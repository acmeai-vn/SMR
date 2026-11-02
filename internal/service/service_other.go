//go:build !windows

package service

import "syscall"

func detachedSysProcAttr() *syscall.SysProcAttr { return nil }
