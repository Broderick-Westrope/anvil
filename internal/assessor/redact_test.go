package assessor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedact(t *testing.T) {
	t.Parallel()

	secrets := []struct {
		name   string
		in     string
		secret string
	}{
		{"aws access key", "export AWS_ACCESS_KEY_ID AKIA" + "ABCDEFGHIJKLMNOP", "AKIA" + "ABCDEFGHIJKLMNOP"},
		{"github classic token", "gh auth login ghp_abcdefghijklmnopqrstuvwxyz0123", "ghp_abcdefghijklmnopqrstuvwxyz0123"},
		{"github oauth token", "x gho_abcdefghijklmnopqrstuvwxyz0123", "gho_abcdefghijklmnopqrstuvwxyz0123"},
		{"github server token", "x ghs_abcdefghijklmnopqrstuvwxyz0123", "ghs_abcdefghijklmnopqrstuvwxyz0123"},
		{"github fine-grained token", "x github_pat_11ABCDEFG0123456789_abcdef", "github_pat_11ABCDEFG0123456789_abcdef"},
		{"openai key", "OPENAI sk-proj-abcdefghij0123456789xyz", "sk-proj-abcdefghij0123456789xyz"},
		{"slack token", "xoxb-1234567890-abcdefghij", "xoxb-1234567890-abcdefghij"},
		{"bearer", "Authorization: Bearer abc.def-ghi_jkl.mnopqrstuvwxyz", "abc.def-ghi_jkl.mnopqrstuvwxyz"},
		{"api key header", "Authorization: Api-Key abcdefghijklmnopqrstuvwx", "abcdefghijklmnopqrstuvwx"},
		{"private key", "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk\n-----END OPENSSH PRIVATE KEY-----", "b3BlbnNzaC1rZXk"},
		{"truncated private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA", "MIIEpAIBAAKCAQEA"},
		{"query password", "https://db.example/?user=a&password=hunter2&x=1", "hunter2"},
		{"query token", "https://api.example/x?token=s3cr3tvalue", "s3cr3tvalue"},
		{"env secret", "CLIENT_SECRET=abc123 ./run", "abc123"},
		{"env token", "GITHUB_TOKEN=tok123 gh pr list", "tok123"},
		{"json api key", `{"api_key": "plainvalue"}`, "plainvalue"},
		{"long run", "x " + "QWxhZGRpbjpvcGVuIHNlc2FtZQ0123456789abcdef", "QWxhZGRpbjpvcGVuIHNlc2FtZQ0123456789abcdef"},
		{"one slash split", "echo AbCdEfGhIjKlMnOpQrSt/UvWxYz0123456789ABCDEF", "AbCdEfGhIjKlMnOpQrSt/UvWxYz0123456789ABCDEF"},
		{"symmetric split", "KEY Zx9Qw3Er5Ty7Ui1Op2As/" + "Df4Gh6Jk8Lz0Xc5Vb7Nm", "Zx9Qw3Er5Ty7Ui1Op2As/" + "Df4Gh6Jk8Lz0Xc5Vb7Nm"},
		{"short and long halves", "x Kq7Lm2Np9Rs4Tv6P/" + "Wx8Yz1Ab3Cd5Ef7Gh9Ij2Kl", "Kq7Lm2Np9Rs4Tv6P/" + "Wx8Yz1Ab3Cd5Ef7Gh9Ij2Kl"},
		{"base64 with plus", "x aB3+dE5fG7hI9jK1lM3n/oP5qR7sT9uV1wX3yZ5aB7", "aB3+dE5fG7hI9jK1lM3n/oP5qR7sT9uV1wX3yZ5aB7"},
		{"long random component", "/srv/data/q83vEjK2mZbW9tYXJzaGFsbGVkSXNU/x", "q83vEjK2mZbW9tYXJzaGFsbGVkSXNU"},
	}
	for _, tt := range secrets {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := Redact(tt.in)
			require.NotContains(t, out, tt.secret)
			require.Contains(t, out, "[REDACTED]")
		})
	}

	survivors := []string{
		"go test ./...",
		"git status",
		"/Users/someone/dev/project/internal/assessor/state.go",
		"go test github.com/Broderick-Westrope/anvil/internal/permission/segment",
		"/var/folders/7h/k5xgt3hd0q1bh5s7wqzk4hzm0000gn/T/TestBuildStateBash2952617373/001",
		"/var/folders/7h/k5xgt3hd0q1bh_s7wqzk4hzm0000gn/T/TestBuildStateEditnew_file_in_new_subdir123/002/main.go",
		"cd /Users/someone/dev/project/internal/permission/assessor_integration_test.go",
		"vim ./internal/agent/tools/edit_permission_request_test.go",
		"go get golang.org/x/tools/gopls/internal/analysis/modernize",
		"gofumpt -l .",
		"https://pkg.go.dev/net/http",
	}
	for _, s := range survivors {
		require.Equal(t, s, Redact(s))
	}
}

func TestRedactSlashedSecret(t *testing.T) {
	t.Parallel()

	secret := "q83vEjK2mZ/bW9tYXJzaGFsbGVkSXNUaGVXYXlUb0dvAbCdEf"
	require.NotContains(t, Redact("echo "+secret), secret)
}
