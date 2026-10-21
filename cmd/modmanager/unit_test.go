package main

import (
	"os"
	"strings"
	"testing"
)

// namespaceDirectives give a unit its own mount namespace (systemd.exec(5)).
// In there, systemd-sysext refresh changes only the service's view of /usr:
// measured on ZimaOS 1.8.0, hide reported done and the dashboard kept the tile.
var namespaceDirectives = []string{
	"ProtectSystem", "ProtectHome", "PrivateTmp", "PrivateDevices", "PrivateMounts",
	"ProtectKernelTunables", "ProtectKernelModules", "ProtectKernelLogs", "ProtectControlGroups",
	"ProtectClock", "ProtectHostname", "ProtectProc", "ProcSubset",
	"ReadOnlyPaths", "ReadWritePaths", "InaccessiblePaths", "ExecPaths", "NoExecPaths",
	"TemporaryFileSystem", "BindPaths", "BindReadOnlyPaths", "RootDirectory", "RootImage",
	"MountAPIVFS", "MountFlags", "PrivateNetwork", "NetworkNamespacePath", "DynamicUser",
	"StateDirectory", "RuntimeDirectory", "CacheDirectory", "LogsDirectory", "ConfigurationDirectory",
}

func TestShippedUnitKeepsTheHostMountNamespace(t *testing.T) {
	b, err := os.ReadFile("../../raw/usr/lib/systemd/system/modmanager.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		for _, d := range namespaceDirectives {
			if strings.EqualFold(strings.TrimSpace(key), d) {
				t.Errorf("modmanager.service sets %s: the service would get a private mount namespace", line)
			}
		}
	}
}
