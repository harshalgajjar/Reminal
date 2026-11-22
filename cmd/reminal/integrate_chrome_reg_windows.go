// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// On Windows a browser of Chrome's family finds a native messaging host in
// the registry, not in its user data folder: the default value of
// HKCU\<browser's key>\NativeMessagingHosts\<host name> is the path of the
// host's manifest. Only the person's own hive (HKCU) is written.

func chromeRegPath(key string) string {
	return key + `\NativeMessagingHosts\` + browserHostName
}

// chromeRegSet points the browser at the manifest.
func chromeRegSet(key, manifest string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, chromeRegPath(key), registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("", manifest)
}

// chromeRegGet is the manifest the browser is pointed at, or "".
func chromeRegGet(key string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, chromeRegPath(key), registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	if err != nil {
		return ""
	}
	return v
}

// chromeRegDel takes the browser's pointer away (one that is not there is
// no error).
func chromeRegDel(key string) error {
	err := registry.DeleteKey(registry.CURRENT_USER, chromeRegPath(key))
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

// chromeRegAppPath is the program Windows starts for exe ("chrome.exe"): the
// default value of its App Paths key, the person's own first and then the
// machine's, or "". Only read.
func chromeRegAppPath(exe string) string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\App Paths\`+exe, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("")
		k.Close()
		if err == nil && v != "" {
			return v
		}
	}
	return ""
}
