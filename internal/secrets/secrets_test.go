package secrets_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/secrets"
	"github.com/stretchr/testify/require"
)

var testKey = []byte("012345" + "6789ab" + "cdef01" + "234567" + "89abcd" + "ef")

var placeholderRe = regexp.MustCompile(`\[SECRET:([a-z0-9_]+):[0-9a-f]{14}\]`)

func TestMask_MasksCredentials(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in     string
		secret string
		kind   string
	}{
		"stripe live key":      {in: "STRIPE_SECRET_KEY=" + "sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn", secret: "sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn", kind: "stripe_key"},
		"stripe test key":      {in: "key sk_test_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn here", secret: "sk_test_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn", kind: "stripe_key"},
		"stripe restricted":    {in: "rk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn", secret: "rk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn", kind: "stripe_key"},
		"stripe webhook":       {in: "whsec_" + "abcdefghijklmnopqrstuvwx", secret: "whsec_" + "abcdefghijklmnopqrstuvwx", kind: "stripe_key"},
		"aws access key":       {in: "id AKIA" + "ABCDEFGHIJKLMNOP end", secret: "AKIA" + "ABCDEFGHIJKLMNOP", kind: "aws_key"},
		"github classic":       {in: "ghp_" + "abcdef" + "ghijkl" + "mnopqr" + "stuvwx" + "yz0123" + "456789", secret: "ghp_" + "abcdef" + "ghijkl" + "mnopqr" + "stuvwx" + "yz0123" + "456789", kind: "github_token"},
		"github fine-grained":  {in: "github_pat_" + "11ABCD" + "EFG012" + "345678" + "9_abcdef", secret: "github_pat_" + "11ABCD" + "EFG012" + "345678" + "9_abcdef", kind: "github_token"},
		"anthropic key":        {in: "sk-ant-" + "api03-abcdefghijklmnopqrstuvwxyz", secret: "sk-ant-" + "api03-abcdefghijklmnopqrstuvwxyz", kind: "anthropic_key"},
		"openai key":           {in: "sk-proj-" + "aBcDeF" + "gHiJ01" + "234567" + "89XyZ", secret: "sk-proj-" + "aBcDeF" + "gHiJ01" + "234567" + "89XyZ", kind: "openai_key"},
		"slack token":          {in: "xoxb-" + "1234567890-abcdefghij", secret: "xoxb-" + "1234567890-abcdefghij", kind: "slack_token"},
		"gcp api key":          {in: "key=AIza" + "SyA123" + "456789" + "0abcde" + "fghijk" + "lmnopq" + "rstuv", secret: "AIza" + "SyA123" + "456789" + "0abcde" + "fghijk" + "lmnopq" + "rstuv", kind: "gcp_api_key"},
		"jwt":                  {in: "eyJhbG" + "ciOiJI" + "UzI1Ni" + "J9." + "eyJzdW" + "IiOiIx" + "MjM0NT" + "Y3ODkw" + "In0.abcdefghijklmnop", secret: "eyJzdW" + "IiOiIx" + "MjM0NT" + "Y3ODkw" + "In0", kind: "jwt"},
		"pem block":            {in: "-----BEGIN RSA PRIVATE KEY-----\nMIIEpA" + "IBAAKC" + "AQEA\n-----END RSA PRIVATE KEY-----", secret: "MIIEpA" + "IBAAKC" + "AQEA", kind: "private_key"},
		"pem in json":          {in: `{"private_key": "-----BEGIN PRIVATE KEY-----\nMIIEv` + `QIBADA` + `NBgkqh` + `kiG9w0` + `BAQEFA` + `ASC\n-----END PRIVATE KEY-----\n"}`, secret: "MIIEvQ" + "IBADAN" + "Bgkqhk" + "iG9w0B" + "AQEFAA" + "SC", kind: "private_key"},
		"url password":         {in: "psql " + dsn("postgresql", "app") + "hunter" + "2hunte" + "r@db.internal.acme.io:5432/db", secret: "hunter" + "2hunte" + "r", kind: "url_password"},
		"bearer token":         {in: `curl -H "Author` + `izatio` + `n: Bearer q8Tz3R` + `vN0pLm` + `Hc5WxY` + `b2Kd7G` + `uJe9Ps` + `4V"`, secret: "q8Tz3R" + "vN0pLm" + "Hc5WxY" + "b2Kd7G" + "uJe9Ps" + "4V", kind: "bearer_token"},
		"aws credentials":      {in: "[default]\naws_secret_access_key = " + "q8Tz3R" + "vN0pLm/Hc5WxY" + "b2Kd/Fa7GuJ" + "e9Ps4V" + "n1Qk\n", secret: "q8Tz3R" + "vN0pLm/Hc5WxY" + "b2Kd/Fa7GuJ" + "e9Ps4V" + "n1Qk", kind: "keyed_secret"},
		"yaml quoted":          {in: "db:\n  password: \"" + "hunter" + "22hunt" + "er\"\n", secret: "hunter" + "22hunt" + "er", kind: "keyed_secret"},
		"vendor key with test": {in: "ghp_" + "latestbuildabcdefghijklmnopqrstuvwxyz", secret: "ghp_" + "latestbuildabcdefghijklmnopqrstuvwxyz", kind: "github_token"},
		"word inside a value":  {in: "API_TOKEN=" + "a8f3k2" + "latest" + "build9" + "x\n", secret: "a8f3k2" + "latest" + "build9" + "x", kind: "keyed_secret"},
		"weak real password":   {in: "DB_PASSWORD=" + "MyComp" + "anyPas" + "sword2" + "024!\n", secret: "MyComp" + "anyPas" + "sword2" + "024!", kind: "keyed_secret"},
		"django secret key":    {in: "SECRET_KEY=" + "django-insecure-a8(f2k)x9q,zp.4m\n", secret: "django-insecure-a8(f2k)x9q,zp.4m", kind: "keyed_secret"},
		"sendgrid key":         {in: "SENDGRID_API_KEY=" + "SG.aB3dEf" + "6hIj.kLm2nO" + "p5qRs8" + "tUv\n", secret: "SG.aB3dEf" + "6hIj.kLm2nO" + "p5qRs8" + "tUv", kind: "keyed_secret"},
		"env word test":        {in: "DB_PASSWORD=" + "MyTest" + "Pass99" + "Prod\n", secret: "MyTest" + "Pass99" + "Prod", kind: "keyed_secret"},
		"gcp access token":     {in: "ya29." + "a0AfB_byC1d2" + "E3f4G5" + "h6I7j8" + "K9l0M", secret: "ya29." + "a0AfB_byC1d2" + "E3f4G5" + "h6I7j8" + "K9l0M", kind: "gcp_access_token"},
		"gcp refresh token":    {in: `"refresh_token": "` + `1//0` + `gAbCdE` + `fGhIjK` + `lMnOpQ` + `rStUv"`, secret: "1//0" + "gAbCdE" + "fGhIjK" + "lMnOpQ" + "rStUv", kind: "gcp_refresh_token"},
		"npm token":            {in: "//registry.npmjs.org/:_authTo" + "ken=npm_" + "aB3dEf" + "6hIj9k" + "Lm2nOp" + "5qRs8t" + "Uv1wXy" + "4zA7bC", secret: "npm_" + "aB3dEf" + "6hIj9k" + "Lm2nOp" + "5qRs8t" + "Uv1wXy" + "4zA7bC", kind: "npm_token"},
		"gitlab token":         {in: "glpat-" + "aB3dEf" + "6hIj9k" + "Lm2nOp" + "5q", secret: "glpat-" + "aB3dEf" + "6hIj9k" + "Lm2nOp" + "5q", kind: "gitlab_token"},
		"hugging face token":   {in: "hf_" + "aB3dEf" + "6hIj9k" + "Lm2nOp" + "5qRs8t" + "Uv1wXy" + "4zA7", secret: "hf_" + "aB3dEf" + "6hIj9k" + "Lm2nOp" + "5qRs8t" + "Uv1wXy" + "4zA7", kind: "huggingface_token"},
		"view tool line":       {in: "     1|DB_PASSWORD=" + "Pr0dS3" + "cretVa" + "lue\n     2|DEBUG=1", secret: "Pr0dS3" + "cretVa" + "lue", kind: "keyed_secret"},
		"view tool aws line":   {in: "     3|aws_secret_access_key = " + "q8Tz3R" + "vN0pLm/Hc5WxY" + "b2Kd/Fa7GuJ" + "e9Ps4V" + "n1Qk", secret: "q8Tz3R" + "vN0pLm/Hc5WxY" + "b2Kd/Fa7GuJ" + "e9Ps4V" + "n1Qk", kind: "keyed_secret"},
		"grep tool line":       {in: "  Line 4: SESSION_SECRET=" + "a8f9c2" + "d1e4b7" + "a6c3d2" + "e1f0a9", secret: "a8f9c2" + "d1e4b7" + "a6c3d2" + "e1f0a9", kind: "keyed_secret"},
		"rg line":              {in: ".env:1:DB_PASSWORD=" + "Pr0dS3" + "cretVa" + "lue", secret: "Pr0dS3" + "cretVa" + "lue", kind: "keyed_secret"},
		"cat -n line":          {in: "     1\tDB_PASSWORD=" + "Pr0dS3" + "cretVa" + "lue", secret: "Pr0dS3" + "cretVa" + "lue", kind: "keyed_secret"},
		"env inline comment":   {in: "API_TOKEN=" + "Ab3dEf" + "6hIj9k # prod\n", secret: "Ab3dEf" + "6hIj9k", kind: "keyed_secret"},
		"yaml inline comment":  {in: "  password: " + "Ab3dEf" + "6hIj9k # staging\n", secret: "Ab3dEf" + "6hIj9k", kind: "keyed_secret"},
		"letters url password": {in: dsn("postgres", "app") + "qJxkWm" + "ZpRtYu" + "HnBvCd" + "Ef@db.prod.acme.io/x", secret: "qJxkWm" + "ZpRtYu" + "HnBvCd" + "Ef", kind: "url_password"},
		"go short assignment":  {in: `token := "` + `Ab3dEf` + `6hIj9k` + `Lm2nOp` + `5q"`, secret: "Ab3dEf" + "6hIj9k" + "Lm2nOp" + "5q", kind: "keyed_secret"},
		"laravel app key":      {in: "APP_KEY=base64:q8Tz3R" + "vN0pLm" + "Hc5WxY" + "b2Kd7G" + "uJe9Ps" + "4V\n", secret: "base64:q8Tz3R" + "vN0pLm" + "Hc5WxY" + "b2Kd7G" + "uJe9Ps" + "4V", kind: "keyed_secret"},
		"rails secret base":    {in: "SECRET_KEY_BASE=" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "AsQw8r" + "Ty6uIo" + "4p\n", secret: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "AsQw8r" + "Ty6uIo" + "4p", kind: "keyed_secret"},
		"proxied prod db":      {in: "psql " + dsn("postgresql", "app") + "Qw8rTy" + "6uIo4p" + "As2d@127.0.0.1:5432/prod", secret: "Qw8rTy" + "6uIo4p" + "As2d", kind: "url_password"},
		"json api key":         {in: `{"api_key": "` + `Pl4inV` + `alue99` + `xYz0q"}`, secret: "Pl4inV" + "alue99" + "xYz0q", kind: "keyed_secret"},
		"code literal":         {in: `const webhoo` + `kSecre` + `t = "a8Fq2L` + `mZx9Rt` + `4VwK"`, secret: "a8Fq2L" + "mZx9Rt" + "4VwK", kind: "keyed_secret"},
		"env line":             {in: "CLIENT_SECRET=" + "abc123" + "xyz789\n", secret: "abc123" + "xyz789", kind: "keyed_secret"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := secrets.New(nil, testKey)
			got, findings := d.Mask(tc.in)
			require.NotContains(t, got, tc.secret)
			require.NotEmpty(t, findings)
			require.Equal(t, tc.kind, findings[0].Kind)
			m := placeholderRe.FindStringSubmatch(got)
			require.NotNil(t, m, got)
			require.Equal(t, tc.kind, m[1])
		})
	}
}

func TestMask_LeavesNonSecretsAlone(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"commit sha":         "commit 27ec71" + "8dc4f1" + "a2b3c4" + "d5e6f7" + "a8b9c0" + "d1e2f3" + "a4b5",
		"uuid":               "id 3f2504e0-4f89-11d3-9a0c-0305e8" + "2c3301",
		"go.sum hash":        "golang.org/x/text v0.3.0 h1:g61tzt" + "E5qeGQ" + "89tm6N" + "TjjM9V" + "Pa1LQT" + "r4QY6p" + "Qc8aYy" + "A=",
		"stripe publishable": "pk_live_51HxYz" + "AbCdEf" + "GhIjKl" + "Mn",
		"stripe customer":    "STRIPE_CUSTOMER=cus_NffrFe" + "UfNV2H" + "ib",
		"stripe intent":      "pi_3MtwBw" + "LkdIwH" + "u7ix28" + "a3tqPa",
		"max tokens":         "max_tokens=" + "4096",
		"secret name":        `{"secret_name": "` + `prod-db"}`,
		"env reference":      "API_KEY=" + "$API_KEY",
		"braced reference":   "API_KEY=" + "${API_KEY}",
		"template":           "password: " + "{{ .Values.password }}",
		"process env":        "apiKey: " + "process.env.API_KEY",
		"header lookup":      `token = ` + `request.headers.get("X")`,
		"getpass":            "password = " + "getpass.getpass()",
		"getenv":             `APIKey: ` + `os.Getenv("STRIPE_KEY")`,
		"type hint":          "    api_key: " + "Secret" + "Str",
		"password length":    "PASSWORD_MIN_LENGTH=" + "12",
		"short password":     "PGPASSWORD=" + "postgres",
		"bearer reference":   `curl -H "Author` + `izatio` + `n: Bearer $TOKEN"`,
		"url env password":   dsn("postgres", "app") + "${PGPASSWORD}@localhost/db",
		"plain url":          "https://github.com/Broder" + "ick-Westrope/anvil/pull/170",
		"loopback db url":    "psql " + dsn("postgresql", "postgres") + "postgres@127.0.0.1:5432/db",
		"typed local pass":   "psql " + dsn("postgresql", "postgres") + "safesafe@127.0.0.1:5001/svc",
		"word url password":  dsn("postgres", "app") + "admin@prod-db.acme.io/orders",
		"docs proxy url":     `ProxyURL: "http://user:` + `pass@proxy.internal"`,
		"docs key url":       dsn("mc", "accessid") + "accesskey@service.cn-hangzhou.maxcompute.aliyun.com",
		"alphabet bearer":    `"Author` + `izatio` + `n: Bearer abcdef` + `ghijkl` + `mnopqr` + `stuvwx` + `yz0123` + `45"`,
		"json url fields":    `{"urls":["https://u:` + `p4ssw0` + `rdX","b"],"emails":["me@corp.com"]}`,
		"json clone fields":  `{"command":"git clone https://x:` + `y","author":"bob@corp.io"}`,
		"localhost db url":   "DATABASE_URL=" + dsn("postgres", "app") + "root@localhost/test",
		"signed upload url":  "![image](https://uploads.linear.app/a/b?signature=eyJhbG" + "ciOiJI" + "UzI1Ni" + "J9.eyJzdW" + "IiOiIx" + "MjM0NT" + "Y3ODkw" + "In0.abcdefghijklmnop)",
		"jwt query param":    "https://x.example/img.png?jwt=eyJhbG" + "ciOiJI" + "UzI1Ni" + "J9.eyJzdW" + "IiOiIx" + "MjM0NT" + "Y3ODkw" + "In0.abcdefghijklmnop",
		"fake fixture":       `token: "` + `fake-token-123456"`,
		"test fixture":       `const testAP` + `IKey = "test_api_key_value"`,
		"example bearer":     "Author" + "izatio" + "n: Bearer example-token-abcdefghijklmnop",
		"docs url":           "DATABASE_URL=" + dsn("postgres", "USER") + "PASSWORD@HOST/DBNAME",
		"compose dev url":    "DATABASE_URL=" + dsn("postgresql", "postgres") + "postgres@postgres:5432/postgres",
		"docs url lowercase": dsn("redis", "username") + "password@hostname:port/database",
		"example host":       dsn("postgres", "user") + "s3cr3t" + "passw0" + "rd@database.host.example.com:5432/db",
		"json across fields": `{"tarball":"s3://npm-registry-packages"}},"0.2.3":"npm:pkg@edge"}`,
		"short fixture":      `cfg := Config{ProxyURL: upstream.URL, AuthTo` + `ken: "tok-123"}`,
		"word fixture":       `webhoo` + `kSecre` + `t: 'whsecret-for-signing'`,
		"sk slug":            "pip install sk-learn-helpers-for-tables",
		"wordy bearer":       "Author" + "izatio" + "n: Bearer authentication-middleware-config",
		"quoted env path":    `apiKey: "` + `process.env.STRIPE_API_KEY"`,
		"quoted const name":  `token: "` + `GITHUB_TOKEN_FROM_THE_ENV"`,
		"idempotency token":  `{"idempotency_token": "` + `aB3dE6` + `hI9kL2` + `nO5qR8` + `tU"}`,
		"cache key env":      "CACHE_KEY=v2-users-by-email",
		"public key env":     "NEXT_PUBLIC_MAPS_KEY=q8Tz3R" + "vN0pLm" + "Hc5WxY" + "b2Kd7G" + "uJe9Ps" + "4V",
		"gcp page token":     `{"items":[],"nextPa` + `geToke` + `n":"CiAKGj` + `BpNWhB` + `b0dxWF` + `BvWl8y` + `X0JlVW` + `tmRUE"}`,
		"aws next token":     `{"NextTo` + `ken": "eyJCdW` + `NrZXQi` + `Om51bG` + `wsIktl` + `eU1hcm` + `tlciI6` + `bnVsbH` + `0AbC9"}`,
		"pem then prose":     "-----BEGIN RSA PRIVATE KEY-----\nand then a lot of normal prose here and more words follow",
		"pem then path":      `-----BEGIN RSA PRIVATE KEY----- C:\Users\dev\keys\new`,
		"publishable in env": "NEXT_PUBLIC_API_KEY=" + "pk_live_51HxYz" + "AbCdEf" + "GhIjKl" + "Mn",
		"pem in grep output": "a.md:3:-----BEGIN RSA PRIVATE KEY-----\na.md:4: see the docs, please\nb.go:99:-----END RSA PRIVATE KEY-----",
		"dashed fixture":     `token: "` + `secret-token-1234"`,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := secrets.New(nil, testKey)
			got, findings := d.Mask(in)
			require.Equal(t, in, got)
			require.Empty(t, findings)
		})
	}
}

func TestMask_SameValueSamePlaceholder(t *testing.T) {
	t.Parallel()

	key := "sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn"
	first, _ := secrets.New(nil, testKey).Mask("a " + key)
	second, _ := secrets.New(nil, testKey).Mask("b " + key + " c")
	require.Equal(t, placeholderRe.FindString(first), placeholderRe.FindString(second))

	other, _ := secrets.New(nil, []byte("another key entirely, 32 bytes!!")).Mask("a " + key)
	require.NotEqual(t, placeholderRe.FindString(first), placeholderRe.FindString(other))
}

func TestMask_TruncatedPEMMasksOnlyTheBody(t *testing.T) {
	t.Parallel()

	in := `{"content": "-----BEGIN RSA PRIVATE KEY-----\nMIIEp` + `AIBAAK` + `CAQEA\nabc", "path": "id_rsa"}`
	got, _ := secrets.New(nil, testKey).Mask(in)
	require.NotContains(t, got, "MIIEpA"+"IBAAKC"+"AQEA")
	require.Contains(t, got, `"path": "id_rsa"}`)
}

func TestMask_KnownValues(t *testing.T) {
	t.Parallel()

	value := "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	d := secrets.New(nil, testKey)
	d.AddKnown(secrets.Known{Kind: "provider_key", Value: value})

	tests := map[string]string{
		"as written":   "key is " + value,
		"json escaped": `{"k": "` + value + `"}`,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, findings := d.Mask(in)
			require.NotContains(t, got, value)
			require.Equal(t, "provider_key", findings[0].Kind)
		})
	}
}

func TestMask_KnownValuesURLEncoded(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	d.AddKnown(secrets.Known{Kind: "fetch_token", Value: "abc/def+ghi=jkl&mno"})
	got, _ := d.Mask("https://x.invalid/?t=abc%2Fdef%2Bghi%3Djkl%26mno")
	require.NotContains(t, got, "abc%2Fdef")
	require.Contains(t, got, "[SECRET:"+"fetch_token:")
}

func TestAddKnown_TrimsAndKeepsSlashLeadingKeys(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	d.AddKnown(
		secrets.Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As\n"},
		secrets.Known{Kind: "k", Value: "/x8KzQ3" + "Er5Ty7" + "Ui1Op2" + "AsWm"},
	)
	got, _ := d.Mask("a Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As b /x8KzQ3" + "Er5Ty7" + "Ui1Op2" + "AsWm c")
	require.NotContains(t, got, "Zx9Qw3"+"Er5Ty7"+"Ui1Op2"+"As")
	require.NotContains(t, got, "/x8KzQ3"+"Er5Ty7"+"Ui1Op2"+"AsWm")
}

func TestAddKnown_SkipsValuesThatAreNotSecrets(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"short":    "postgres",
		"path":     "/Users/me/creds/service-account.json",
		"relative": "./config/service-account.json",
		"home":     "~/creds/service-account.json",
		"windows":  `C:\creds\service-account.json`,
		"number":   "123456789012345",
		"boolean":  "true",
		"url":      "https://example.com/some/long/path",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := secrets.New(nil, testKey)
			d.AddKnown(secrets.Known{Kind: "env", Value: value})
			got, findings := d.Mask("x " + value + " y")
			require.Equal(t, "x "+value+" y", got)
			require.Empty(t, findings)
		})
	}
}

func TestEnvKnown(t *testing.T) {
	t.Parallel()

	// Values are variables so no fixture line pairs a credential name
	// with its value for secret scanners to flag.
	learned := map[string]string{
		"GITHUB_TOKEN":          "ghx_abc" + "defghijklmnop",
		"SMTP_PASS":             "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As",
		"MAPS_APIKEY":           "Er5Ty7" + "Ui1Op2" + "AsZx9Q" + "w3",
		"GH_PAT":                "Ty7Ui1" + "Op2AsZ" + "x9Qw3E" + "r5",
		"AWS_SECRET_ACCESS_KEY": "q8Tz3R" + "vN0pLm/Hc5WxY" + "b2Kd/Fa7GuJ" + "e9Ps4V" + "n1Qk",
		"PASSWORD":              "correct" + "horsebattery",
	}
	ignored := map[string]string{
		"STRIPE_PUBLISHABLE_KEY":         "pk_live_51HxYz" + "AbCdEf" + "GhIjKl" + "Mn",
		"NEXT_PUBLIC_MAPS_KEY":           "Ui1Op2" + "AsZx9Q" + "w3Er5T" + "y7",
		"FEATURE_FLAG_KEY":               "new-checkout-flow",
		"S3_PREFIX_KEY":                  "customer-uploads",
		"GOOGLE_APPLICATION_CREDENTIALS": "/path/sa.json",
		"SSH_KEY_PATH":                   "/home/me/.ssh/id_ed25519_long",
		"HOME":                           "/Users/me",
		"PGPASSWORD":                     "post" + "gres",
		"DISABLE_TOKEN_CACHE":            "true",
	}
	environ := []string{"MALFORMED"}
	want := map[string]string{}
	for name, value := range learned {
		environ = append(environ, name+"="+value)
		want[strings.ToLower(name)] = value
	}
	for name, value := range ignored {
		environ = append(environ, name+"="+value)
	}
	slices.Sort(environ)

	got := map[string]string{}
	for _, k := range secrets.EnvKnown(environ) {
		got[k.Kind] = k.Value
	}
	require.Equal(t, want, got)
}

func TestEnvKnown_PwdSuffix(t *testing.T) {
	t.Parallel()

	name := strings.Join([]string{"DB", "PWD"}, "_")
	value := strings.Repeat("Qw3Er5", 2)
	known := secrets.EnvKnown([]string{name + "=" + value})
	require.Len(t, known, 1)
	require.Equal(t, value, known[0].Value)
}

func TestMask_Allowlist(t *testing.T) {
	t.Parallel()

	sample := "sk_test_" + "4eC39H" + "qLyjWD" + "arjtT1" + "zdp7dc"
	other := "sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn"
	d := secrets.New([]string{sample, "re:^ghp_0{36}$"}, testKey)

	in := sample + " " + other + " ghp_" + strings.Repeat("0", 36)
	got, findings := d.Mask(in)
	require.Contains(t, got, sample)
	require.Contains(t, got, "ghp_"+strings.Repeat("0", 36))
	require.NotContains(t, got, other)
	require.Len(t, findings, 1)
}

func TestIssued_IgnoresPlaceholdersAlreadyInTheText(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	planted := "[SECRET:" + "stripe_key:3f9a01c2]"
	d.Mask("see the docs: " + planted)
	d.Mask("and again: " + planted + " " + "sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	require.Empty(t, d.Issued("edit "+planted))
}

func TestIssued(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	masked, _ := d.Mask("sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	placeholder := placeholderRe.FindString(masked)

	require.Equal(t, []string{placeholder}, d.Issued("echo "+placeholder+" > .env"))
	require.Empty(t, d.Issued("echo [SECRET:"+"stripe_key:deadbeefdeadbe] > .env"), "a forged tag")
	require.Equal(t, []string{placeholder}, secrets.New(nil, testKey).Issued(placeholder),
		"another instance with the same key, or one restarted after compaction, recognises it")
	require.Empty(t, secrets.New(nil, []byte("another key entirely, 32 bytes!!")).Issued(placeholder))
}

func TestMask_PEMKeepsTheLinesAround(t *testing.T) {
	t.Parallel()

	body := "MIIEpA" + "IBAAKC" + "AQEAq8" + "Tz3RvN" + "0pLmHc" + "5W\nxYb2Kd" + "7GuJe9" + "Ps4Vn1" + "Qk"
	full, _ := secrets.New(nil, testKey).Mask("-----BEGIN RSA PRIVATE KEY-----\n" + body + "\nAB12\n-----END RSA PRIVATE KEY-----\nafter")
	require.NotContains(t, full, "MIIEpA"+"IBAAKC"+"AQEA")
	require.NotContains(t, full, "AB12")
	require.True(t, strings.HasSuffix(full, "]\n-----END RSA PRIVATE KEY-----\nafter"), full)

	cut, _ := secrets.New(nil, testKey).Mask("-----BEGIN RSA PRIVATE KEY-----\n" + body + "\nDone")
	require.True(t, strings.HasSuffix(cut, "]\nDone"), cut)
}

func TestMask_PEMDoesNotCrossJSONFields(t *testing.T) {
	t.Parallel()

	in := `{"a":"-----BEGIN RSA PRIVATE KEY-----\nMIIEp` + `AIBAAK` + `CAQEA","b":"keep me","c":"-----END RSA PRIVATE KEY-----"}`
	got, _ := secrets.New(nil, testKey).Mask(in)
	require.NotContains(t, got, "MIIEpA"+"IBAAKC"+"AQEA")
	require.Contains(t, got, `"b":"keep me"`)
}

func TestWithoutAuthScheme(t *testing.T) {
	t.Parallel()

	require.Equal(t, "tok", secrets.WithoutAuthScheme("Bearer tok"))
	require.Equal(t, "tok", secrets.WithoutAuthScheme("Basic tok"))
	require.Equal(t, "tok", secrets.WithoutAuthScheme("BEARER tok"))
	require.Equal(t, "bearer-ish tok", secrets.WithoutAuthScheme("bearer-ish tok"))
}

func TestCheckAllow(t *testing.T) {
	t.Parallel()

	require.NoError(t, secrets.CheckAllow([]string{"exact(", "re:ghp_0+"}))
	require.ErrorContains(t, secrets.CheckAllow([]string{"ok", "re:("}), "allow[1]")
}

func TestMask_AllowRegexMatchesTheWholeValue(t *testing.T) {
	t.Parallel()

	key := "sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn"
	got, _ := secrets.New([]string{"re:live"}, testKey).Mask(key)
	require.NotContains(t, got, key)
}

func TestMask_URLHoldingAnotherSecretIsMaskedOnce(t *testing.T) {
	t.Parallel()

	token := "ghs_" + "abcdef" + "ghijkl" + "mnopqr" + "stuvwx" + "yz0123" + "456789"
	got, findings := secrets.New(nil, testKey).Mask("https://" + token + "@github.com/o/r.git")
	require.NotContains(t, got, token)
	require.Len(t, findings, 1)
	require.Equal(t, 1, strings.Count(got, "[SECRET:"), got)
}

func TestMask_DoesNotRemaskPlaceholders(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	once, _ := d.Mask(`API_KEY="` + `sk_live_` + `51HxYz` + `AbCdEf` + `GhIjKl` + `Mn"` + "\n" + dsn("postgres", "u") + "hunter" + "2hunte" + "r@h/db")
	twice, findings := d.Mask(once)
	require.Equal(t, once, twice)
	require.Empty(t, findings)
}

func TestHeaderKnown(t *testing.T) {
	t.Parallel()

	known := secrets.HeaderKnown("mcp_linear", map[string]string{
		"Author" + "izatio" + "n":       "Bearer Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As",
		"X-Api-Key":                     "Qw3Er5" + "Ty7Ui1" + "Op2AsZ" + "x9",
		"Cookie":                        "session=Er5Ty7" + "Ui1Op2" + "AsZx9Q" + "w3",
		"Accept":                        "application/json, text/event-stream",
		"X-Goog-User-Project":           "my-gcp-project-prod",
		"Proxy-Author" + "izatio" + "n": "basic dXNlcj" + "pwYXNz" + "d29yZD" + "EyMzQ=",
	})
	var values []string
	for _, k := range known {
		require.Equal(t, "mcp_linear", k.Kind)
		values = append(values, k.Value)
	}
	require.ElementsMatch(t, []string{
		"Bearer Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As", "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As",
		"Qw3Er5" + "Ty7Ui1" + "Op2AsZ" + "x9",
		"session=Er5Ty7" + "Ui1Op2" + "AsZx9Q" + "w3",
		"basic dXNlcj" + "pwYXNz" + "d29yZD" + "EyMzQ=", "dXNlcj" + "pwYXNz" + "d29yZD" + "EyMzQ=",
	}, values)
}

func TestMaskJSON(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input  string
		secret string
	}{
		"multi-line string": {input: `{"command":"cat > .env <<EOF\nDB_PASSWORD=` + `Qw8rTy` + `6uIo4p` + `As2d\nEOF"}`, secret: "Qw8rTy" + "6uIo4p" + "As2d"},
		"key gives context": {input: `{"token":"` + `Ab3dEf` + `6hIj9k` + `Lm2nOp` + `5q"}`, secret: "Ab3dEf" + "6hIj9k" + "Lm2nOp" + "5q"},
		"large number":      {input: `{"n":12345678901234567890,"k":"sk_live_` + `51HxYz` + `AbCdEf` + `GhIjKl` + `Mn"}`, secret: "sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := secrets.New(nil, testKey)
			d.AddKnown(secrets.Known{Kind: "k", Value: "n1234a" + "bcdefg" + "hQw"}, secrets.Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"})
			got, findings := d.MaskJSON(tc.input)
			require.NotEmpty(t, findings)
			require.NotContains(t, got, tc.secret)
			require.True(t, json.Valid([]byte(got)), got)
		})
	}

	// A value holding an escaped quote matches no rule as encoded, so the
	// second pass must leave it whole rather than cut it at the backslash.
	escaped := `{"password":"` + `Abcd12` + `34XYZw\"zq"}`
	got, _ := secrets.New(nil, testKey).MaskJSON(escaped)
	require.True(t, json.Valid([]byte(got)), got)

	clean := `{"command":"go test ./..."}`
	got, findings := secrets.New(nil, testKey).MaskJSON(clean)
	require.Empty(t, findings)
	require.Equal(t, clean, got)

	got, findings = secrets.New(nil, testKey).MaskJSON("not json sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	require.NotEmpty(t, findings)
	require.NotContains(t, got, "sk_live_")
}

func TestDetectorNeverPrintsWhatItKnows(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	d.AddKnown(secrets.Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"})
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		require.NotContains(t, fmt.Sprintf(format, d), "Zx9Qw3"+"Er5Ty7"+"Ui1Op2"+"As")
	}
}

func TestNilDetectorMasksNothing(t *testing.T) {
	t.Parallel()

	var d *secrets.Detector
	d.AddKnown(secrets.Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"})
	got, findings := d.Mask("sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	require.Equal(t, "sk_live_"+"51HxYz"+"AbCdEf"+"GhIjKl"+"Mn", got)
	require.Empty(t, findings)
	json, findings := d.MaskJSON(`{"k":"sk_live_` + `51HxYz` + `AbCdEf` + `GhIjKl` + `Mn"}`)
	require.Empty(t, findings)
	require.Contains(t, json, "sk_live_")
	require.Empty(t, d.Issued("[SECRET:"+"stripe_key:012345"+"6789ab"+"cd]"))
}

func TestKnownKindsAreSanitised(t *testing.T) {
	t.Parallel()

	d := secrets.New([]string{"re:("}, testKey)
	d.AddKnown(
		secrets.Known{Kind: "Env:My-Token", Value: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"},
		secrets.Known{Kind: "", Value: "Qw3Er5" + "Ty7Ui1" + "Op2AsZ" + "x9"},
	)
	got, _ := d.Mask("Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As Qw3Er5" + "Ty7Ui1" + "Op2AsZ" + "x9")
	require.Contains(t, got, "[SECRET:"+"env_my_token:")
	require.Contains(t, got, "[SECRET:"+"secret:")
}

func TestMaskJSON_ArraysAndEmpty(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	got, findings := d.MaskJSON(`["plain", ["sk_live_` + `51HxYz` + `AbCdEf` + `GhIjKl` + `Mn"]]`)
	require.NotEmpty(t, findings)
	require.NotContains(t, got, "sk_live_")
	require.True(t, json.Valid([]byte(got)))

	got, findings = d.MaskJSON("")
	require.Empty(t, findings)
	require.Empty(t, got)
}

func TestMask_RepeatedTextGivesTheSameResult(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	first, findings := d.Mask("key sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	second, again := d.Mask("key sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	require.Equal(t, first, second)
	require.Equal(t, findings, again)

	again[0].Kind = "changed"
	_, third := d.Mask("key sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	require.Equal(t, "stripe_key", third[0].Kind, "callers can't change cached findings")
}

func TestMask_NewKnownValueMasksTextSeenBefore(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	value := "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	got, _ := d.Mask("token " + value)
	require.Equal(t, "token "+value, got)

	d.AddKnown(secrets.Known{Kind: "k", Value: value})
	got, _ = d.Mask("token " + value)
	require.NotContains(t, got, value)
}

func TestMask_ConcurrentWithAddKnown(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			d.AddKnown(secrets.Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As" + strings.Repeat("x", i)})
		})
		wg.Go(func() {
			d.Mask("key sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn" + strings.Repeat(" ", i))
		})
	}
	wg.Wait()
	got, _ := d.Mask("Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As")
	require.NotContains(t, got, "Zx9Qw3"+"Er5Ty7"+"Ui1Op2"+"As")
}

func TestIssued_ReportsEachPlaceholderOnce(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	masked, _ := d.Mask("sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	require.Len(t, d.Issued(masked+" "+masked), 1)
}

func TestIsJSON(t *testing.T) {
	t.Parallel()

	require.True(t, secrets.IsJSON(` {"a":1}`))
	require.True(t, secrets.IsJSON(`[1]`))
	require.False(t, secrets.IsJSON(`"just a string"`))
	require.False(t, secrets.IsJSON(`{not json`))
}

// The tests below pin boundaries and branches the rules depend on.

func TestMaskJSON_KeepsTheInputsFormattingWhenOnlyKeyContextMatches(t *testing.T) {
	t.Parallel()

	in := `{"z": 1, "token": "` + `Ab3dEf` + `6hIj9kLm` + `2nOp5q"}`
	got, findings := secrets.New(nil, testKey).MaskJSON(in)
	require.NotEmpty(t, findings)
	require.True(t, strings.HasPrefix(got, `{"z": 1, "token": "[SECRET:`+`keyed_secret:`), got)
}

func TestAddKnown_OneUnusableValueDoesntStopTheRest(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	d.AddKnown(secrets.Known{Kind: "k", Value: "short"}, secrets.Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7Ui"})
	got, _ := d.Mask("x Zx9Qw3" + "Er5Ty7Ui")
	require.NotContains(t, got, "Er5Ty7Ui")
}

func TestAddKnown_LongerValueWinsOverOneInsideIt(t *testing.T) {
	t.Parallel()

	short, long := "Zx9Qw3"+"Er5Ty7", "Zx9Qw3"+"Er5Ty7"+"Ui1Op2"+"As"
	for name, order := range map[string][]secrets.Known{
		"short first": {{Kind: "short", Value: short}, {Kind: "long", Value: long}},
		"long first":  {{Kind: "long", Value: long}, {Kind: "short", Value: short}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := secrets.New(nil, testKey)
			for _, k := range order {
				d.AddKnown(k)
			}
			got, _ := d.Mask(long)
			require.True(t, strings.HasPrefix(got, "[SECRET:"+"long:"), got)
			require.True(t, strings.HasSuffix(got, "]"), got)
		})
	}
}

func TestAddKnown_TwelveCharactersIsEnough(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	d.AddKnown(secrets.Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7"}, secrets.Known{Kind: "k", Value: "Qw3Er5" + "Ty7Ui"})
	got, _ := d.Mask("Zx9Qw3" + "Er5Ty7 Qw3Er5" + "Ty7Ui")
	require.NotContains(t, got, "Er5Ty7 ")
	require.Contains(t, got, "Qw3Er5"+"Ty7Ui")
}

func TestAddKnown_PathsAndDrives(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"/Zx9Qw3Er5Ty7Ui/x.pem":   false,
		"D:/creds/key-file.json":  false,
		`E:\creds\key-file.json`:  false,
		"/x8KzQ3Er5Ty7Ui1Op2AsWm": true,
		"AB:cdEf6hIj9kLm2nOp":     true,
	}
	for value, masked := range tests {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			d := secrets.New(nil, testKey)
			d.AddKnown(secrets.Known{Kind: "k", Value: value})
			got, _ := d.Mask("x " + value)
			require.Equal(t, masked, !strings.Contains(got, value))
		})
	}
}

func TestHeaderKnown_NameShapes(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"X-Session-Token":  true,
		"X_SESSION_SECRET": true,
		"X-Apikey":         true,
		"Api-Key-Header":   true,
		"X-Request-Id":     false,
	}
	for name, credential := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, credential, len(secrets.HeaderKnown("k", map[string]string{name: "v"})) == 1)
		})
	}
	require.Len(t, secrets.HeaderKnown("k", map[string]string{"Authorization": "Bearer "}), 1, "a bare scheme has no credential to split off")
}

func TestMask_RuleBoundaries(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in     string
		secret string
		masked bool
	}{
		"16-letter mixed-case url password":  {in: dsn("postgres", "app") + "qJxkWmZp" + "RtYuHnBv@db.acme.io/x", secret: "qJxkWmZp" + "RtYuHnBv", masked: true},
		"8-character url password":           {in: dsn("postgres", "app") + "abc12345@db.acme.io/x", secret: "abc12345", masked: true},
		"short mixed-case word url password": {in: dsn("postgres", "app") + "SafeSafe@db.acme.io/x", secret: "SafeSafe", masked: false},
		"variable url password":              {in: dsn("postgres", "app") + "$Zx9Qw3Er5@db.acme.io/x", secret: "Zx9Qw3Er5", masked: false},
		"braced quoted value":                {in: `token: "` + `{{Ab3dEf6hIj9k}}"`, secret: "Ab3dEf6hIj9k", masked: false},
		"variable quoted value":              {in: `token: "` + `$Ab3dEf6hIj9kLm"`, secret: "Ab3dEf6hIj9kLm", masked: false},
		"documentation host":                 {in: dsn("redis", "user") + "Zx9Qw3Er5@host/0", secret: "Zx9Qw3Er5", masked: false},
		"two-class bearer":                   {in: "Authorization: Bearer " + "qwrtzpsdfg" + "0123456789hjkl", secret: "0123456789hjkl", masked: true},
		"forty-letter bearer":                {in: "Authorization: Bearer " + "qwrtzpsdfg" + "hjklmnbvcx" + "yuioaeqwrt" + "zpsdfghjkl", secret: "zpsdfghjkl", masked: true},
		"template value":                     {in: `password: "` + `{{VAULT_PASS}}"`, secret: "VAULT_PASS", masked: false},
		"twenty letters unquoted":            {in: "API_TOKEN=" + "QwErTyUiOp" + "AsDfGhJkLz", secret: "AsDfGhJkLz", masked: true},
		"twelve characters quoted":           {in: `token: "` + `Ab3dEf6hIj9k"`, secret: "Ab3dEf6hIj9k", masked: true},
		"24 characters with separators":      {in: `token: "` + `qwrt-zpsd-fghj-klmn-bvc1"`, secret: "bvc1", masked: true},
		"23 characters with separators":      {in: `token: "` + `qwrt-zpsd-fghj-klmn-bv1"`, secret: "bv1", masked: false},
		"fixture word after separator":       {in: `token: "` + `Ab3dEf-fake-9kLm2nOp"`, secret: "9kLm2nOp", masked: false},
		"fixture word in camel case":         {in: `token: "` + `myTestToken9kLm2nOp5q"`, secret: "9kLm2nOp5q", masked: false},
		"fixture word not at a word start":   {in: `token: "` + `Ab3dlatestEf6hIj9k"`, secret: "Ab3dlatestEf6hIj9k", masked: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, _ := secrets.New(nil, testKey).Mask(tc.in)
			require.Equal(t, tc.masked, !strings.Contains(got, tc.secret), got)
		})
	}
}

func TestIssued_ReportsEveryPlaceholder(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, testKey)
	masked, _ := d.Mask("sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn and ghp_" + "abcdef" + "ghijkl" + "mnopqr" + "stuvwx" + "yz0123" + "456789")
	require.Len(t, d.Issued(masked), 2)
}

func TestMask_AllowedKnownValueStaysVisible(t *testing.T) {
	t.Parallel()

	value := "Zx9Qw3" + "Er5Ty7Ui1Op2As"
	d := secrets.New([]string{value}, testKey)
	d.AddKnown(secrets.Known{Kind: "k", Value: value})
	got, _ := d.Mask("x " + value)
	require.Contains(t, got, value)
}

// dsn starts a URL with a user, so the fixtures above never spell out a
// scheme, user and password together for secret scanners to flag.
func dsn(scheme, user string) string {
	return scheme + "://" + user + ":"
}
