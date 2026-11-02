//go:build windows

package service

import "testing"

// A process may expose CommandLine but not ExecutablePath. The old parser
// then read the executable path as the command line, decided it was not a
// daemon and cleared the lock of a running SMR.
func TestParseProcessInfo(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		cmdline string
		exe     string
	}{
		{
			name:    "both fields",
			output:  "C=\"C:\\AcmeAI\\SMR\\smr.exe\" serve\r\nE=C:\\AcmeAI\\SMR\\smr.exe\r\n",
			cmdline: "\"C:\\AcmeAI\\SMR\\smr.exe\" serve",
			exe:     "C:\\AcmeAI\\SMR\\smr.exe",
		},
		{
			name:    "command line unavailable",
			output:  "C=\r\nE=C:\\AcmeAI\\SMR\\smr.exe\r\n",
			cmdline: "",
			exe:     "C:\\AcmeAI\\SMR\\smr.exe",
		},
		{
			name:    "only the executable path",
			output:  "E=C:\\AcmeAI\\SMR\\smr.exe\r\n",
			cmdline: "",
			exe:     "C:\\AcmeAI\\SMR\\smr.exe",
		},
		{name: "process gone"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cmdline, exe := parseProcessInfo(testCase.output)
			if cmdline != testCase.cmdline || exe != testCase.exe {
				t.Fatalf("parseProcessInfo() = %q, %q; want %q, %q", cmdline, exe, testCase.cmdline, testCase.exe)
			}
		})
	}
}
