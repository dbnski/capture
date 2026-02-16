package main

import (
	"bytes"
	"testing"
)

func TestWriteInSingleLineUnsafe(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no newlines",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "single newline in middle",
			input:    "hello\nworld",
			expected: "hello world",
		},
		{
			name:     "multiple newlines",
			input:    "hello\nworld\ntest",
			expected: "hello world test",
		},
		{
			name:     "consecutive newlines",
			input:    "hello\n\nworld",
			expected: "hello  world",
		},
		{
			name:     "newline at start",
			input:    "\nhello",
			expected: " hello",
		},
		{
			name:     "newline at end",
			input:    "hello\n",
			expected: "hello ",
		},
		{
			name:     "newline at both ends",
			input:    "\nhello\n",
			expected: " hello ",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "only newlines",
			input:    "\n\n\n",
			expected: "   ",
		},
		{
			name:     "complex multiline text",
			input:    "SELECT *\nFROM users\nWHERE id = 1",
			expected: "SELECT * FROM users WHERE id = 1",
		},
		{
			name:     "text with trailing newlines",
			input:    "query text\n\n",
			expected: "query text  ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeInSingleLineUnsafe(&buf, tt.input)

			got := buf.String()
			if got != tt.expected {
				t.Errorf("writeInSingleLineUnsafe() = %q, want %q", got, tt.expected)
			}
		})
	}
}
