//go:build linux

package quadlet

import (
	"strings"
	"testing"

	"go.podman.io/podman/v6/pkg/systemd/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			name:    "TN-01 empty host port",
			content: "[Container]\nImage=test\nSocketActivationPort=:80\n",
			wantErr: "requires explicit host port",
		},
		{
			name:    "TN-02 port range",
			content: "[Container]\nImage=test\nSocketActivationPort=8080-8090:80-90\n",
			wantErr: "does not support port ranges",
		},
		{
			name:    "TN-03 UDP protocol",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80/udp\n",
			wantErr: "only supports TCP",
		},
		{
			name:    "TN-04 host port zero",
			content: "[Container]\nImage=test\nSocketActivationPort=0:80\n",
			wantErr: "between 1 and 65535",
		},
		{
			name:    "TN-05 host port out of range",
			content: "[Container]\nImage=test\nSocketActivationPort=65536:80\n",
			wantErr: "between 1 and 65535",
		},
		{
			name:    "TN-06 missing container port",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:\n",
			wantErr: "non-empty container port",
		},
		{
			name:    "TN-07 invalid format",
			content: "[Container]\nImage=test\nSocketActivationPort=invalid\n",
			wantErr: "invalid port number",
		},
		{
			name:    "TN-08 internal port collision",
			content: "[Container]\nImage=test\nPublishPort=9090:80\nSocketActivationPort=8080:80\nSocketActivationInternalPort=9090\n",
			wantErr: "internal port 9090 already in use",
		},
		{
			name:    "TN-10 specifier in port",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:%i\n",
			wantErr: "systemd specifiers in port numbers",
		},
		{
			name:    "TN-12 multiple entries",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPort=9090:90\n",
			wantErr: "at most one entry",
		},
		{
			name:    "TN-13 self-loop default",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:8080\n",
			wantErr: "self-loop",
		},
		{
			name:    "TN-13b self-loop explicit",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationInternalPort=8080\n",
			wantErr: "self-loop",
		},
		{
			name:    "TN-15 Network=none",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80\nNetwork=none\n",
			wantErr: "Network=none unsupported",
		},
		{
			name:    "TN-16 scope ID",
			content: "[Container]\nImage=test\nSocketActivationPort=[fe80::1%eth0]:8080:80\n",
			wantErr: "cannot parse",
		},
		{
			name:     "TN-17 template no DefaultInstance",
			content:  "[Container]\nImage=test\nSocketActivationPort=8080:80\n",
			filename: "test@.container",
			wantErr:  "not supported in v1",
		},
		{
			name:    "TN-18 unknown option",
			content: "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPortOptions=--bogus\n",
			wantErr: "unknown option",
		},
		{
			name:    "TN-21 explicit internal vs PP",
			content: "[Container]\nImage=test\nPublishPort=8080:90\nSocketActivationPort=9090:80\nSocketActivationInternalPort=8080\n",
			wantErr: "internal port 8080 already in use",
		},
		{
			name:    "SAP019 conflict with PP",
			content: "[Container]\nImage=test\nPublishPort=8080:90\nSocketActivationPort=8080:80\n",
			wantErr: "conflicts with PublishPort",
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
				require.NotNil(t, warnings, "expected warning")
				assert.Contains(t, warnings.Error(), tt.wantWarn)
			}
			_ = warnings
		})
	}
}

func TestSocketActivationPort_Positive(t *testing.T) {
	t.Run("TC-01 basic", func(t *testing.T) {
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
		assert.True(t, strings.Contains(execStart[0], "--publish 127.0.0.1:80:80"))

		restart, _ := svc.Lookup("Service", "Restart")
		assert.Equal(t, "on-failure", restart)

		socketUnit := extras[0]
		assert.Equal(t, "test.socket", socketUnit.Filename)
		listen, _ := socketUnit.Lookup("Socket", "ListenStream")
		assert.Equal(t, "8080", listen)
		serviceRef, _ := socketUnit.Lookup("Socket", "Service")
		assert.Equal(t, "test-proxy.service", serviceRef)

		proxyUnit := extras[1]
		assert.Equal(t, "test-proxy.service", proxyUnit.Filename)
		requires, _ := proxyUnit.Lookup("Unit", "Requires")
		assert.Equal(t, "test.service", requires)
	})

	t.Run("TC-06 with options", func(t *testing.T) {
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
		assert.True(t, strings.Contains(execStart[0], "--timeout=30s"), "options should be present even when proxyd is absent")
	})

	t.Run("TC-07 explicit internal", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationInternalPort=18080\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--publish 127.0.0.1:18080:80"))
	})

	t.Run("TC-10 coexist with PP", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nPublishPort=9000:8080\nSocketActivationPort=8443:443\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.Len(t, extras, 2)

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--publish 9000:8080"))
		assert.True(t, strings.Contains(execStart[0], "--publish 127.0.0.1:443:443"))
	})

	t.Run("TC-15 default internal", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "--publish 127.0.0.1:80:80"))
	})

	t.Run("TC-16 collision avoidance", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nPublishPort=80:8080\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "127.0.0.1:81:80"), "internal 80 collides with PP container 80 -> search to 81")
	})

	t.Run("TC-04 IPv6 loopback", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=[::1]:8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.Len(t, extras, 2)

		socketUnit := extras[0]
		listen, _ := socketUnit.Lookup("Socket", "ListenStream")
		assert.Equal(t, "[::1]:8080", listen)
	})

	t.Run("TN-19 Network=host warning", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nNetwork=host\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, warnings, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.NotNil(t, svc)
		require.Empty(t, extras, "no socket/proxy for Network=host")

		require.NotNil(t, warnings)
		assert.Contains(t, warnings.Error(), "Network=host does not support")

		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.False(t, strings.Contains(execStart[0], "--publish 127.0.0.1"), "no --publish injected for Network=host")
	})

	t.Run("TC-08 template with DefaultInstance", func(t *testing.T) {
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
	})

	t.Run("Restart precedence guard", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n[Service]\nRestart=no\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		restart, _ := svc.Lookup("Service", "Restart")
		assert.Equal(t, "no", restart, "user Restart=no must not be overwritten")
	})

	t.Run("single ExecStart", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nPublishPort=9000:8080\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)

		execStarts := svc.LookupAll("Service", "ExecStart")
		assert.Len(t, execStarts, 1, "must have exactly one ExecStart")
	})

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
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "PrivateDevices"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "RestrictSUIDSGID"))
		assert.Equal(t, "yes", getKey(t, proxyUnit, "Service", "RestrictRealtime"))
		assert.Equal(t, "@system-service", getKey(t, proxyUnit, "Service", "SystemCallFilter"))

		_, hasMDE := proxyUnit.Lookup("Service", "MemoryDenyWriteExecute")
		assert.False(t, hasMDE, "MemoryDenyWriteExecute must NOT be present")
		_, hasRN := proxyUnit.Lookup("Service", "RestrictNamespaces")
		assert.False(t, hasRN, "RestrictNamespaces must NOT be present")
	})

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
		assert.True(t, strings.Contains(execPre[0], "--publish 127.0.0.1:80:80"))

		socketUnit := extras[0]
		assert.Equal(t, "test-pod.socket", socketUnit.Filename)
		listen, _ := socketUnit.Lookup("Socket", "ListenStream")
		assert.Equal(t, "8080", listen)

		proxyUnit := extras[1]
		assert.Equal(t, "test-pod-proxy.service", proxyUnit.Filename)
		requires, _ := proxyUnit.Lookup("Unit", "Requires")
		assert.Equal(t, "test-pod.service", requires)
	})

	t.Run("Network=container:XXX error", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nNetwork=container:other\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported")
	})

	t.Run("Network=.container error", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nNetwork=other.container\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		// Error comes from addNetworks (unit not found in map) before SAP validation
		assert.Contains(t, err.Error(), "was not found")
	})

	t.Run("ExposeHostPort in usedPorts", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nExposeHostPort=80\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "127.0.0.1:81:80"), "internal 80 collides with EHP 80 → should search to 81")
	})

	t.Run("ExposeHostPort range in usedPorts", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nExposeHostPort=80-82\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		svc, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		execStart := svc.LookupAll("Service", "ExecStart")
		require.Len(t, execStart, 1)
		assert.True(t, strings.Contains(execStart[0], "127.0.0.1:83:80"), "internal 80 collides with EHP 80-82 → should search to 83")
	})

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

	t.Run("isPortRange invalid EHP rejected", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nExposeHostPort=invalid\nSocketActivationPort=8080:80\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid port format", "invalid EHP rejected by main ConvertContainer before SAP")
	})

	t.Run("template proxy naming", func(t *testing.T) {
		u := makeContainerUnit("test@.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\n[Install]\nDefaultInstance=1\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test@.container": {ServiceName: "test@", ResourceName: "systemd-test"},
		}

		_, _, err, extras := ConvertContainer(u, unitsInfoMap, false)
		require.NoError(t, err)
		require.Len(t, extras, 2)

		proxyUnit := extras[1]
		assert.Equal(t, "test-proxy@.service", proxyUnit.Filename, "template proxy must be test-proxy@.service, not test@-proxy@.service")
	})

	t.Run("duplicate InternalPort error", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationInternalPort=90\nSocketActivationInternalPort=91\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be specified more than once")
	})

	t.Run("unknown option not --timeout without value", func(t *testing.T) {
		u := makeContainerUnit("test.container", "[Container]\nImage=test\nSocketActivationPort=8080:80\nSocketActivationPortOptions=--unknown-flag\n")
		unitsInfoMap := map[string]*UnitInfo{
			"test.container": {ServiceName: "test", ResourceName: "systemd-test"},
		}

		_, _, err, _ := ConvertContainer(u, unitsInfoMap, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown option")
	})

	t.Run("valid option --buffer-size accepted", func(t *testing.T) {
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
}

func getKey(t *testing.T, uf *parser.UnitFile, group, key string) string {
	t.Helper()
	v, ok := uf.Lookup(group, key)
	require.True(t, ok, "key %q not found in group %q", key, group)
	return v
}
