package bashtion

import (
	"reflect"
	"testing"
)

func TestSplitCommandArgs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
		wantErr  bool
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
			wantErr:  false,
		},
		{
			name:     "whitespace only",
			input:    "   \t\n  ",
			expected: nil,
			wantErr:  false,
		},
		{
			name:     "simple flags",
			input:    "--timing -T",
			expected: []string{"--timing", "-T"},
			wantErr:  false,
		},
		{
			name:     "double quotes with space",
			input:    `--filter "test foo bar" --verbose`,
			expected: []string{"--filter", "test foo bar", "--verbose"},
			wantErr:  false,
		},
		{
			name:     "single quotes with space",
			input:    `--filter 'test foo bar' -T`,
			expected: []string{"--filter", "test foo bar", "-T"},
			wantErr:  false,
		},
		{
			name:     "escaped characters",
			input:    `--filter foo\ bar`,
			expected: []string{"--filter", "foo bar"},
			wantErr:  false,
		},
		{
			name:     "unmatched double quote",
			input:    `--filter "foo bar`,
			expected: nil,
			wantErr:  true,
		},
		{
			name:     "unmatched single quote",
			input:    `--filter 'foo bar`,
			expected: nil,
			wantErr:  true,
		},
		{
			name:     "trailing backslash",
			input:    `--filter foo\`,
			expected: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SplitCommandArgs(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SplitCommandArgs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("SplitCommandArgs() = %v, want %v", got, tt.expected)
			}
		})
	}
}
