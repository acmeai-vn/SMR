// Package fileperm applies user-only permissions to SMR files. On Windows,
// chmod does not change ACLs, so the Windows implementation uses icacls.
package fileperm
