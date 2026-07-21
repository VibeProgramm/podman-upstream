//go:build linux

package quadlet

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/podman/v6/pkg/systemd/parser"
)

func makeContainerUnit(filename, content string) *parser.UnitFile {
	u := parser.NewUnitFile()
	u.Filename = filename
	if err := u.Parse(content); err != nil {
		panic(err)
	}
	return u
}

func TestSocketActivationPort_Negative(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantErr  string
		wantWarn string
		filename string
	}{
		{
			name:    "empty host port",
			content: "[Container]\nImage=test\nSocketActivationPort=:80\n",
			wantErr: "requires explicit host port",
		},
		{
			name:    "port range",
			content: "[Container]\nImage=test\nSocketActivationPort=8080-8090:80-90\n",
			wantErr: "does not support port ranges",
		},
		{
			name:    "UDP protocol",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80/udp\n",
			wantErr: "only supports TCP",
		},
		{
			name:    "host port zero",
			content: "[Container]\nImage=test\nSocketActivationPort=0:80\n",
			wantErr: "between 1 and 65535",
		},
		{
			name:    "host port out of range",
			content: "[Container]\nImage=test\nSocketActivationPort=65536:80\n",
			wantErr: "between 1 and 65535",
		},
		{
			name:    "missing container port",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:\n",
			wantErr: "non-empty container port",
		},
		{
			name:    "invalid format",
			content: "[Container]\nImage=test\nSocketActivationPort=invalid\n",
			wantErr: "invalid port number",
		},
		{
			name:    "specifier in port",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:%i\n",
			wantErr: "systemd specifiers in port numbers",
		},
		{
			name:    "Network=none",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80\nNetwork=none\n",
			wantErr: "unsupported",
		},
		{
			name:    "scope ID",
			content: "[Container]\nImage=test\nSocketActivationPort=[fe80::1%eth0]:8080:80\n",
			wantErr: "cannot parse",
		},
		{
			name:     "template no DefaultInstance",
			content:  "[Container]\nImage=test\nSocketActivationPort=8080:80\n",
			filename: "test@.container",
			wantErr:  "not supported in v1",
		},
		{
			name:    "unknown option",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPortOptions=--bogus\n",
			wantErr: "unknown option",
		},
		{
			name:    "SAP019 conflict with PP",
			content: "[Container]\nImage=test\nPublishPort=8080:90\nSocketActivationPort=8080:80\n",
			wantErr: "conflicts with PublishPort",
		},
		{
			name:    "duplicate host port",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPort=8080:443\n",
			wantErr: "duplicate host port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filename := tt.filename
			if filename == "" {
				filename = "test.container"
			}
			u := makeContainerUnit(filename, tt.content)
			unitsInfoMap := map[string]*UnitInfo{
				filename: {ServiceName: "test", ResourceName: "systemd-test"},
			}

			svc, warnings, err, extras := ConvertContainer(u, unitsInfoMap, false)
			_ = warnings

			if tt.wantErr != "" {
				if assert.Error(t, err, "expected error containing %q", tt.wantErr) {
					assert.Contains(t, err.Error(), tt.wantErr)
				}
				assert.Nil(t, svc)
				assert.Empty(t, extras)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, svc)
			}

			if tt.wantWarn != "" {
				require.NotNil(t, warnings)
				assert.Contains(t, warnings.Error(), tt.wantWarn)
			}
		})
	}
}

func TestSocketActivationPort_Positive(t *testing.T) {
	// TC-01: Single port — new naming scheme with port in filename
	t.Run("single port basic", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.NotNil(t, svc)
		require.Len(t, extras, 2)

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--publish 127.0.0.1:1024:80"))

		restart, _ := svc.Lookup("Service", "Restart")
		assert.Equal(t, "on-failure", restart)

		socketUnit := extras[0]
		assert.Equal(t, "test-8080.socket", socketUnit.Filename)
		listen, _ := socketUnit.Lookup("Socket", "ListenStream")
		assert.Equal(t, "127.0.0.1:8080", listen)
		serviceRef, _ := socketUnit.Lookup("Socket", "Service")
		assert.Equal(t, "test-8080-proxy.service", serviceRef)

		proxyUnit := extras[1]
		assert.Equal(t, "test-8080-proxy.service", proxyUnit.Filename)
	})

	// Multi-port: 2 entries → 4 extras
	t.Run("multi port", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPort=8443:443\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.NotNil(t, svc)
		require.Len(t, extras, 4)

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--publish 127.0.0.1:1024:80"))
		assert.True(t, strings.Contains(execStart[0], "--publish 127.0.0.1:1025:443"))

		// Socket filenames include port
		assert.Equal(t, "test-8080.socket", extras[0].Filename)
		assert.Equal(t, "test-8080-proxy.service", extras[1].Filename)
		assert.Equal(t, "test-8443.socket", extras[2].Filename)
		assert.Equal(t, "test-8443-proxy.service", extras[3].Filename)
	})

	// Options in ExecStart
	t.Run("with options", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPortOptions=--timeout=30s\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--timeout=30s"))
	})

	// Floor=1024: containerPort 80 → internalPort 1024
	t.Run("floor 1024", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "127.0.0.1:1024:80"), "containerPort 80 < 1024 → floor to 1024")
	})

	// Floor=1024 not applied when containerPort ≥ 1024
	t.Run("no floor needed", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:8080\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		// containerPort 8080 ≥ 1024 but equals hostPort (8080) → increment to 8081
		assert.True(t, strings.Contains(execStart[0], "127.0.0.1:8081:8080"))
	})

	// Collision avoidance: internalPort occupied → search upward
	t.Run("collision avoidance", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nExposeHostPort=1024\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		// ExposeHostPort=1024 → usedPorts has 1024
		// SAP containerPort 80 → internal starts at max(1024, 1024)=1024 → 1024 occupied → search → 1025
		assert.True(t, strings.Contains(execStart[0], "127.0.0.1:1025:80"))
	})

	// Network=host → warning, skip
	t.Run("Network=host warning", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nNetwork=host\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, warnings, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.NotNil(t, svc)
		require.Empty(t, extras)

		require.NotNil(t, warnings)
		assert.Contains(t, warnings.Error(), "Network=host does not support")
	})

	// Template with DefaultInstance
	t.Run("template with DefaultInstance", func(t *testing.T) {
		u := makeContainerUnit("test@.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n[Install]\nDefaultInstance=1\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test@.container": {ServiceName: "test@", ResourceName: "systemd-test"},
		}

		svc, warnings, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.NotNil(t, svc)
		require.Len(t, extras, 2)

		require.NotNil(t, warnings)
		assert.Contains(t, warnings.Error(), "only the default instance is supported")

		assert.Equal(t, "test-8080@.socket", extras[0].Filename)
		assert.Equal(t, "test-8080-proxy@.service", extras[1].Filename)
	})

	// Restart precedence guard
	t.Run("Restart precedence guard", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n[Service]\nRestart=no\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		restart, _ := svc.Lookup("Service", "Restart")
		assert.Equal(t, "no", restart)
	})

	// Single ExecStart
	t.Run("single ExecStart", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPort=8443:443\nPublishPort=9000:8080\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		execStarts := svc.LookupAll("Service", "ExecStart")
		assert.Len(t, execStarts, 1, "must have exactly one ExecStart")
	})

	// Proxy hardening
	t.Run("proxy hardening", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "PrivateTmp"))
		assert.Equal(t, "strict", getKey(t, proxyUnit, "Service", "ProtectSystem"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "ProtectHome"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "NoNewPrivileges"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "ProtectKernelTunables"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "ProtectKernelModules"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "ProtectControlGroups"))
		assert.Equal(t, "AF_INET AF_INET6 AF_UNIX", getKey(t, proxyUnit, "Service", "RestrictAddressFamilies"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "LockPersonality"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "RestrictRealtime"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "PrivateDevices"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "RestrictSUIDSGID"))
		assert.Equal(t, "@system-service", getKey(t, proxyUnit, "Service", "SystemCallFilter"))
		assert.Equal(t, "EPERM", getKey(t, proxyUnit, "Service", "SystemCallErrorNumber"))
		cbsVal, hasCBS := proxyUnit.Lookup("Service", "CapabilityBoundingSet")
		assert.True(t, hasCBS, "CapabilityBoundingSet must be present")
		assert.Equal(t, "", cbsVal, "CapabilityBoundingSet must be empty (drop all caps)")

		_, hasMDE := proxyUnit.Lookup("Service", "MemoryDenyWriteExecute")
		assert.False(t, hasMDE, "MemoryDenyWriteExecute must NOT be present")
		_, hasRN := proxyUnit.Lookup("Service", "RestrictNamespaces")
		assert.False(t, hasRN, "RestrictNamespaces must NOT be present")

		// No readiness probe in v2
		execPre := proxyUnit.LookupAll("Service", "ExecStartPre")
		assert.Empty(t, execPre, "no readiness probe")
	})

	// ConvertPod
	t.Run("ConvertPod basic", func(t *testing.T) {
		u := makeContainerUnit("test.pod", "[Pod]\nPodName=test\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.pod": {ServiceName: "test-pod", ResourceName: "systemd-test-pod"},
		}

		svc, _, err, extras := ConvertPod(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.NotNil(t, svc)
		require.Len(t, extras, 2)

		execPre := svc.LookupAll("Service", "ExecStartPre")
		require.Len(t, execPre, 1)
		assert.True(t, strings.Contains(execPre[0], "--publish 127.0.0.1:1024:80"))

		assert.Equal(t, "test-pod-8080.socket", extras[0].Filename)
	})

	// Network=container:xxx error
	t.Run("Network=container:XXX error", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nNetwork=container:other\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported")
	})

	// ExposeHostPort in usedPorts
	t.Run("ExposeHostPort collision", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nExposeHostPort=1024\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "127.0.0.1:1025:80"), "EHP 1024 + floor 1024 → search to 1025")
	})

	// PodmanArgs network warning
	t.Run("PodmanArgs network warning", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nPodmanArgs=--network=none\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, warnings, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.NotNil(t, warnings)
		assert.Contains(t, warnings.Error(), "PodmanArgs")
	})

	// Valid --buffer-size option
	t.Run("valid --buffer-size option", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPortOptions=--buffer-size=65536\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.Len(t, extras, 2)
		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--buffer-size=65536"))
	})

	t.Run("PodmanArgs --publish= parse error warning", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nPodmanArgs=--publish=::::\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}
		_, warnings, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.NotNil(t, warnings)
		assert.Contains(t, warnings.Error(), "PodmanArgs")
	})

	t.Run("PodmanArgs --publish= exact conflict error", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nPodmanArgs=--publish=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}
		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with PodmanArgs")
	})

	t.Run("PodmanArgs --publish= range conflict error", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8081:80\nPodmanArgs=--publish=8080-8082:80-82\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}
		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with PodmanArgs")
	})

	t.Run("PodmanArgs --publish= no conflict warning", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nPodmanArgs=--publish=9090:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}
		_, warnings, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.NotNil(t, warnings)
		assert.Contains(t, warnings.Error(), "PodmanArgs")
	})

	t.Run("Pod PodmanArgs --publish= conflict", func(t *testing.T) {
		u := makeContainerUnit("test.pod", "[Pod]\nPodName=test\nSocketActivationPort=8080:80\nPodmanArgs=--publish=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.pod": {ServiceName: "test-pod", ResourceName: "systemd-test-pod"},
		}
		_, _, err, _ := ConvertPod(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with PodmanArgs")
	})
}

func getKey(t *testing.T, uf *parser.UnitFile, group, key string) string {
	t.Helper()
	v, ok := uf.Lookup(group, key)
	require.True(t, ok, "key %q not found in group %q", key, group)
	return v
}
