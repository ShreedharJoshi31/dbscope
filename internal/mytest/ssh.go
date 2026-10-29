package mytest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
)

// SSHImage is the base of the bastion; sshd is installed when it starts.
const SSHImage = "public.ecr.aws/docker/library/alpine:3.22"

// Bastion is an SSH server that allows port forwarding, for tunnel tests.
type Bastion struct {
	Host string
	Port int
	User string
	// KeyFile holds the user's ed25519 key, unencrypted; Key is the same key.
	KeyFile string
	Key     ed25519.PrivateKey
	// KnownHosts lists the server's host key; HostKey is that key.
	KnownHosts string
	HostKey    ssh.PublicKey
}

// TunnelTarget is how the bastion reaches a database container's published
// port on this machine.
func TunnelTarget(port int) (string, int) { return "host.docker.internal", port }

// SSH starts a bastion with one user, "tunnel", who logs in with a key generated here.
func SSH(t testing.TB) Bastion {
	t.Helper()
	if testing.Short() || os.Getenv("DBSCOPE_SKIP_SSH") != "" {
		t.Skip("integration test")
	}
	ctx := context.Background()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	script := `apk add -q openssh-server && ssh-keygen -A -q && adduser -D tunnel && passwd -u tunnel >/dev/null 2>&1; ` +
		`mkdir -p /home/tunnel/.ssh && echo "$AUTHORIZED_KEY" > /home/tunnel/.ssh/authorized_keys && ` +
		`chown -R tunnel /home/tunnel/.ssh && chmod 700 /home/tunnel/.ssh && chmod 600 /home/tunnel/.ssh/authorized_keys && ` +
		`exec /usr/sbin/sshd -D -e -o AllowTcpForwarding=yes -o PasswordAuthentication=no`
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: SSHImage, Entrypoint: []string{"sh", "-c", script}, ExposedPorts: []string{"22/tcp"},
			Env:        map[string]string{"AUTHORIZED_KEY": string(ssh.MarshalAuthorizedKey(sshPub))},
			WaitingFor: wait.ForLog("Server listening").WithStartupTimeout(2 * time.Minute),
			// Docker Desktop resolves host.docker.internal itself; Linux needs the mapping.
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.ExtraHosts = append(hc.ExtraHosts, "host.docker.internal:host-gateway")
			},
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("no ssh container: %v", err)
	}
	t.Cleanup(func() { c.Terminate(context.Background()) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "22/tcp")
	n, _ := strconv.Atoi(port.Port())
	r, err := c.CopyFileFromContainer(ctx, "/etc/ssh/ssh_host_ed25519_key.pub")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(r)
	r.Close()
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	b := Bastion{Host: host, Port: n, User: "tunnel", Key: key, HostKey: hostKey,
		KeyFile: filepath.Join(dir, "id_ed25519"), KnownHosts: filepath.Join(dir, "known_hosts")}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.KeyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	line := knownHostsLine(host, n, hostKey)
	if err := os.WriteFile(b.KnownHosts, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

func knownHostsLine(host string, port int, key ssh.PublicKey) string {
	name := host
	if port != 22 {
		name = "[" + host + "]:" + strconv.Itoa(port)
	}
	return name + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + "\n"
}
