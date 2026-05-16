package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFilterList(t *testing.T) {
	t.Parallel()

	allItems := []string{"a", "b", "c", "d"}

	tests := map[string]struct {
		input    []string
		allItems []string
		want     []string
		wantErr  bool
		wantNil  bool
	}{
		"nil returns all items": {
			input:    nil,
			allItems: allItems,
			want:     allItems,
		},
		"wildcard returns all items": {
			input:    []string{"*"},
			allItems: allItems,
			want:     allItems,
		},
		"empty returns empty slice": {
			input:    []string{},
			allItems: allItems,
		},
		"positive returns intersection in allItems order": {
			input:    []string{"c", "a"},
			allItems: allItems,
			want:     []string{"a", "c"},
		},
		"positive with unknown items returns only known intersection": {
			input:    []string{"a", "z"},
			allItems: allItems,
			want:     []string{"a"},
		},
		"negative returns all minus negated": {
			input:    []string{"!a", "!c"},
			allItems: allItems,
			want:     []string{"b", "d"},
		},
		"single negative returns all minus that item": {
			input:    []string{"!b"},
			allItems: allItems,
			want:     []string{"a", "c", "d"},
		},
		"mixed positive then negative returns error": {
			input:    []string{"a", "!b"},
			allItems: allItems,
			wantErr:  true,
			wantNil:  true,
		},
		"mixed negative then positive returns error": {
			input:    []string{"!a", "b"},
			allItems: allItems,
			wantErr:  true,
			wantNil:  true,
		},
		"nil allItems with positive input returns empty": {
			input:    []string{"a"},
			allItems: nil,
		},
		"nil allItems with nil input returns nil": {
			input:   nil,
			wantNil: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseFilterList(tt.input, tt.allItems)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tt.wantNil {
				require.Nil(t, result)
			} else if tt.want != nil {
				require.Equal(t, tt.want, result)
			} else {
				require.Empty(t, result)
			}
		})
	}
}

func TestValidateFilterList(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input   []string
		wantErr bool
	}{
		"nil is valid":                      {input: nil},
		"empty is valid":                    {input: []string{}},
		"wildcard is valid":                 {input: []string{"*"}},
		"all positive is valid":             {input: []string{"a", "b", "c"}},
		"single positive is valid":          {input: []string{"a"}},
		"all negative is valid":             {input: []string{"!a", "!b", "!c"}},
		"single negative is valid":          {input: []string{"!a"}},
		"positive then negative is invalid": {input: []string{"a", "!b"}, wantErr: true},
		"negative then positive is invalid": {input: []string{"!a", "b"}, wantErr: true},
		"mixed with many items is invalid":  {input: []string{"a", "b", "!c", "d"}, wantErr: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ValidateFilterList(tt.input)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
