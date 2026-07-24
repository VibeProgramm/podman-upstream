//go:build linux

package quadlet

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/podman/v6/pkg/systemd/parser"
)

func makePodUnit(filename, content string) *parser.UnitFile {
	return makeContainerUnit(filename, content)
}

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
			name:    "--exit-idle-time rejected",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPortOptions=--exit-idle-time=30s\n",
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
		assert.Equal(t, "8080", listen)
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
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPortOptions=--connections-max=100\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--connections-max=100"))
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

		// Verify template proxy has %i in Requires/After/PartOf
		proxyUnit := extras[1]
		requires, ok := proxyUnit.Lookup("Unit", "Requires")
		require.True(t, ok, "proxy must have Requires")
		assert.Equal(t, "test@%i.service", requires, "template proxy Requires must use %%i")
		after, ok := proxyUnit.Lookup("Unit", "After")
		require.True(t, ok, "proxy must have After")
		assert.Equal(t, "test@%i.service", after, "template proxy After must use %%i")
		partOf, ok := proxyUnit.Lookup("Unit", "PartOf")
		require.True(t, ok, "proxy must have PartOf")
		assert.Equal(t, "test@%i.service", partOf, "template proxy PartOf must use %%i")
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
		assert.Contains(t, execStarts[0], "--publish 127.0.0.1:1024:80", "must contain SAP port 80")
		assert.Contains(t, execStarts[0], "--publish 127.0.0.1:1025:443", "must contain SAP port 443")
		assert.Contains(t, execStarts[0], "--publish 9000:8080", "must contain regular PublishPort")
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

	// Network=container:xxx error
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

	// Valid --connections-max option
	t.Run("valid --connections-max option", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPortOptions=--connections-max=100\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.Len(t, extras, 2)
		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--connections-max=100"))
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

	t.Run("PodmanArgs --publish= no conflict no warning", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nPodmanArgs=--publish=9090:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}
		_, warnings, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		// No conflict → no warning (the old code warned on every --publish= even without conflict)
		if warnings != nil {
			assert.NotContains(t, warnings.Error(), "conflicts")
		}
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

func TestSocketIdleTimeout(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketIdleTimeout=20m\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, _, extras := ConvertContainer(u, unitsInfoMap, false)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--exit-idle-time=20m"), "proxy should have --exit-idle-time=20m")
	})

	t.Run("without SAP errors", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketIdleTimeout=20m\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SocketIdleTimeout requires SocketActivationPort")
	})

	t.Run("conflicts with Restart=always", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketIdleTimeout=20m\n[Service]\nRestart=always\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Restart=always is incompatible")
	})

	t.Run("StopWhenUnneeded default for SAP", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		stopWhenUnneeded, ok := svc.Lookup("Unit", "StopWhenUnneeded")
		assert.True(t, ok, "StopWhenUnneeded should be set for SAP containers")
		assert.Equal(t, "yes", stopWhenUnneeded)
	})

	t.Run("StopWhenUnneeded user override preserved", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n[Unit]\nStopWhenUnneeded=no\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		stopWhenUnneeded, ok := svc.Lookup("Unit", "StopWhenUnneeded")
		assert.True(t, ok, "StopWhenUnneeded should be set")
		assert.Equal(t, "no", stopWhenUnneeded, "user override should be preserved")
	})

	t.Run("zero means unset", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketIdleTimeout=0\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, _, extras := ConvertContainer(u, unitsInfoMap, false)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.False(t, strings.Contains(execStart[0], "--exit-idle-time"), "proxy should NOT have --exit-idle-time when value is 0")
	})

	t.Run("zero seconds means unset", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketIdleTimeout=0s\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, _, extras := ConvertContainer(u, unitsInfoMap, false)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.False(t, strings.Contains(execStart[0], "--exit-idle-time"), "proxy should NOT have --exit-idle-time when value is 0s")
	})

	t.Run("invalid duration errors", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketIdleTimeout=foo\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid duration")
	})

	t.Run("negative duration treated as unset", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketIdleTimeout=-5m\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, _, extras := ConvertContainer(u, unitsInfoMap, false)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.False(t, strings.Contains(execStart[0], "--exit-idle-time"), "proxy should NOT have --exit-idle-time for negative duration")
	})

	t.Run("multi-port idle timeout", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPort=8443:443\nSocketIdleTimeout=15m\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, _, extras := ConvertContainer(u, unitsInfoMap, false)
		require.Len(t, extras, 4) // 2 sockets + 2 proxies

		proxy8080 := extras[1]
		execStart8080 := proxy8080.LookupAll("Service", "ExecStart")
		require.Len(t, execStart8080, 1)
		assert.Contains(t, execStart8080[0], "--exit-idle-time=15m", "port 8080 proxy should have idle timeout")

		proxy8443 := extras[3]
		execStart8443 := proxy8443.LookupAll("Service", "ExecStart")
		require.Len(t, execStart8443, 1)
		assert.Contains(t, execStart8443[0], "--exit-idle-time=15m", "port 8443 proxy should have idle timeout")
	})

	t.Run("restart on-failure works with idle timeout", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketIdleTimeout=10m\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		restart, ok := svc.Lookup("Service", "Restart")
		assert.True(t, ok, "SAP should default Restart=on-failure")
		assert.Equal(t, "on-failure", restart)
	})

	t.Run("Pod basic idle timeout", func(t *testing.T) {
		u := makePodUnit("test.pod", "[Pod]\nPodName=test\nSocketActivationPort=8080:80\nSocketIdleTimeout=10m\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.pod": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, _, extras := ConvertPod(u, unitsInfoMap, false)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.Contains(t, execStart[0], "--exit-idle-time=10m", "pod proxy should have idle timeout")
	})

	t.Run("Pod zero means unset", func(t *testing.T) {
		u := makePodUnit("test.pod", "[Pod]\nPodName=test\nSocketActivationPort=8080:80\nSocketIdleTimeout=0\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.pod": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, _, extras := ConvertPod(u, unitsInfoMap, false)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		execStart := proxyUnit.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.False(t, strings.Contains(execStart[0], "--exit-idle-time"), "pod proxy should NOT have --exit-idle-time when value is 0")
	})

	t.Run("Pod StopWhenUnneeded user override preserved", func(t *testing.T) {
		u := makePodUnit("test.pod", "[Pod]\nPodName=test\nSocketActivationPort=8080:80\n[Unit]\nStopWhenUnneeded=no\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.pod": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertPod(u, unitsInfoMap, false)
		require.NoError(t, err)
		stopWhenUnneeded, ok := svc.Lookup("Unit", "StopWhenUnneeded")
		assert.True(t, ok, "StopWhenUnneeded should be set")
		assert.Equal(t, "no", stopWhenUnneeded, "user override should be preserved")
	})
}
