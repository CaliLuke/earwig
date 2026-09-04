//go:build live

package livevalidation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	opikTestcontainersEnv = "EARWIG_OPIK_TESTCONTAINERS"
	opikImage             = "ghcr.io/comet-ml/opik/opik-backend:2.2.52"
)

type opikTestStack struct {
	network    *testcontainers.DockerNetwork
	containers []testcontainers.Container
}

func TestMain(m *testing.M) {
	if os.Getenv(opikTestcontainersEnv) != "1" || os.Getenv("EARWIG_OPIK_URL") != "" {
		os.Exit(m.Run())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	stack, endpoint, err := startOpikTestStack(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Opik Testcontainers stack: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("EARWIG_OPIK_URL", endpoint); err != nil {
		fmt.Fprintf(os.Stderr, "set Opik endpoint: %v\n", err)
		cleanupOpikTestStack(stack)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Opik Testcontainers endpoint: %s\n", endpoint)

	code := m.Run()
	if err := cleanupOpikTestStack(stack); err != nil {
		fmt.Fprintf(os.Stderr, "stop Opik Testcontainers stack: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func configureDockerHost(ctx context.Context) error {
	if os.Getenv("DOCKER_HOST") != "" {
		return nil
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return nil
	}

	output, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("detect Docker endpoint: %w: %s", err, strings.TrimSpace(string(output)))
	}
	host := strings.TrimSpace(string(output))
	if host == "" {
		return errors.New("detect Docker endpoint: current context has no Docker host")
	}
	if err := os.Setenv("DOCKER_HOST", host); err != nil {
		return fmt.Errorf("set Docker endpoint: %w", err)
	}
	if strings.Contains(host, "/.colima/") && os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE") == "" {
		if err := os.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/var/run/docker.sock"); err != nil {
			return fmt.Errorf("set Testcontainers socket override: %w", err)
		}
	}
	return nil
}

func startOpikTestStack(ctx context.Context) (*opikTestStack, string, error) {
	if err := configureDockerHost(ctx); err != nil {
		return nil, "", err
	}
	testNetwork, err := network.New(ctx, network.WithAttachable())
	if err != nil {
		return nil, "", fmt.Errorf("create network: %w", err)
	}
	stack := &opikTestStack{network: testNetwork}
	fail := func(err error) (*opikTestStack, string, error) {
		cleanupErr := cleanupOpikTestStack(stack)
		return nil, "", errors.Join(err, cleanupErr)
	}

	if _, err := stack.start(ctx, "mysql", testcontainers.ContainerRequest{
		Image: "mysql:8.4.2",
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "opik",
			"MYSQL_DATABASE":      "opik",
			"MYSQL_USER":          "opik",
			"MYSQL_PASSWORD":      "opik",
		},
		WaitingFor: wait.ForExec([]string{"mysqladmin", "ping", "-h", "127.0.0.1", "--silent"}).
			WithStartupTimeout(5 * time.Minute),
	}); err != nil {
		return fail(err)
	}

	if _, err := stack.start(ctx, "redis", testcontainers.ContainerRequest{
		Image:      "redis:7.2.4-alpine3.19",
		Cmd:        []string{"redis-server", "--requirepass", "opik"},
		WaitingFor: wait.ForExec([]string{"redis-cli", "-a", "opik", "ping"}).WithStartupTimeout(2 * time.Minute),
	}); err != nil {
		return fail(err)
	}

	if _, err := stack.start(ctx, "zookeeper", testcontainers.ContainerRequest{
		Image: "zookeeper:3.9.4",
		Env: map[string]string{
			"JVMFLAGS":                   "-Xmx512m",
			"ZOO_4LW_COMMANDS_WHITELIST": "srvr,ruok",
		},
		WaitingFor: wait.ForLog("Started AdminServer on address").WithStartupTimeout(2 * time.Minute),
	}); err != nil {
		return fail(err)
	}

	if _, err := stack.start(ctx, "clickhouse", testcontainers.ContainerRequest{
		Image: "clickhouse/clickhouse-server:26.3.16.16-alpine",
		Env: map[string]string{
			"CLICKHOUSE_DB":                        "opik",
			"CLICKHOUSE_USER":                      "opik",
			"CLICKHOUSE_PASSWORD":                  "opik",
			"CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1",
		},
		Files: []testcontainers.ContainerFile{
			{
				Reader:            strings.NewReader(clickHouseServerConfig),
				ContainerFilePath: "/etc/clickhouse-server/config.d/earwig-test.xml",
				FileMode:          0o644,
			},
			{
				Reader:            strings.NewReader(clickHouseUserConfig),
				ContainerFilePath: "/etc/clickhouse-server/users.d/earwig-test.xml",
				FileMode:          0o644,
			},
		},
		WaitingFor: wait.ForExec([]string{"wget", "--spider", "-q", "http://127.0.0.1:8123/ping"}).
			WithStartupTimeout(5 * time.Minute),
	}); err != nil {
		return fail(err)
	}

	if _, err := stack.start(ctx, "minio", testcontainers.ContainerRequest{
		Image: "minio/minio:RELEASE.2025-03-12T18-04-18Z",
		Env: map[string]string{
			"MINIO_ROOT_USER":     "THAAIOSFODNN7EXAMPLE",
			"MINIO_ROOT_PASSWORD": "LESlrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
		Cmd: []string{"server", "--console-address", ":9090", "/data"},
		WaitingFor: wait.ForExec([]string{"curl", "-f", "http://127.0.0.1:9000/minio/health/live"}).
			WithStartupTimeout(2 * time.Minute),
	}); err != nil {
		return fail(err)
	}
	_, err = stack.start(ctx, "backend", testcontainers.ContainerRequest{
		Image:        opikImage,
		ExposedPorts: []string{"8080/tcp"},
		Cmd:          []string{"bash", "-c", "./run_db_migrations.sh && ./provision_agent_insights_readonly_user.sh && ./entrypoint.sh"},
		Env: map[string]string{
			"STATE_DB_PROTOCOL":                 "jdbc:mysql://",
			"STATE_DB_URL":                      "mysql:3306/opik?createDatabaseIfNotExist=true&rewriteBatchedStatements=true&connectionTimeZone=UTC&forceConnectionTimeZoneToSession=true",
			"STATE_DB_DATABASE_NAME":            "opik",
			"STATE_DB_USER":                     "opik",
			"STATE_DB_PASS":                     "opik",
			"ANALYTICS_DB_MIGRATIONS_URL":       "jdbc:clickhouse://clickhouse:8123",
			"ANALYTICS_DB_MIGRATIONS_USER":      "opik",
			"ANALYTICS_DB_MIGRATIONS_PASS":      "opik",
			"ANALYTICS_DB_PROTOCOL":             "HTTP",
			"ANALYTICS_DB_HOST":                 "clickhouse",
			"ANALYTICS_DB_PORT":                 "8123",
			"ANALYTICS_DB_DATABASE_NAME":        "opik",
			"ANALYTICS_DB_USERNAME":             "opik",
			"ANALYTICS_DB_PASS":                 "opik",
			"REDIS_URL":                         "redis://:opik@redis:6379/",
			"AWS_ACCESS_KEY_ID":                 "THAAIOSFODNN7EXAMPLE",
			"AWS_SECRET_ACCESS_KEY":             "LESlrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"IS_MINIO":                          "true",
			"S3_URL":                            "http://minio:9000",
			"PYTHON_EVALUATOR_URL":              "http://127.0.0.1:1",
			"TOGGLE_OPIK_AI_ENABLED":            "false",
			"TOGGLE_GUARDRAILS_ENABLED":         "false",
			"TOGGLE_WELCOME_WIZARD_ENABLED":     "false",
			"LLM_MODEL_REGISTRY_REMOTE_ENABLED": "false",
			"OPIK_USAGE_REPORT_ENABLED":         "false",
			"JAVA_OPTS":                         "-Dliquibase.propertySubstitutionEnabled=true -XX:+UseG1GC -XX:MaxRAMPercentage=80.0",
		},
		WaitingFor: wait.ForHTTP("/health-check").WithPort("8080/tcp").WithStartupTimeout(10 * time.Minute),
	})
	if err != nil {
		return fail(err)
	}

	proxy, err := stack.start(ctx, "proxy", testcontainers.ContainerRequest{
		Image:        "nginx:1.29.3-alpine",
		ExposedPorts: []string{"5173/tcp"},
		Files: []testcontainers.ContainerFile{{
			Reader:            strings.NewReader(opikProxyConfig),
			ContainerFilePath: "/etc/nginx/nginx.conf",
			FileMode:          0o644,
		}},
		WaitingFor: wait.ForHTTP("/health").WithPort("5173/tcp").WithStartupTimeout(2 * time.Minute),
	})
	if err != nil {
		return fail(err)
	}

	endpoint, err := proxy.PortEndpoint(ctx, "5173/tcp", "http")
	if err != nil {
		return fail(fmt.Errorf("resolve Opik proxy endpoint: %w", err))
	}
	return stack, endpoint, nil
}

func (s *opikTestStack) start(ctx context.Context, alias string, request testcontainers.ContainerRequest) (testcontainers.Container, error) {
	request.Networks = []string{s.network.Name}
	request.NetworkAliases = map[string][]string{s.network.Name: {alias}}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: request,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", alias, err)
	}
	s.containers = append(s.containers, container)
	return container, nil
}

func cleanupOpikTestStack(stack *opikTestStack) error {
	if stack == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var errs []error
	for i := len(stack.containers) - 1; i >= 0; i-- {
		if err := stack.containers[i].Terminate(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if stack.network != nil {
		if err := stack.network.Remove(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

const clickHouseServerConfig = `<clickhouse>
    <macros>
        <shard>1</shard>
        <replica>clickhouse</replica>
        <cluster>cluster</cluster>
    </macros>
    <zookeeper>
        <node>
            <host>zookeeper</host>
            <port>2181</port>
        </node>
    </zookeeper>
    <zookeeper_path>/clickhouse</zookeeper_path>
    <zookeeper_session_timeout_ms>30000</zookeeper_session_timeout_ms>
    <distributed_ddl>
        <path>/clickhouse/task_queue/ddl</path>
    </distributed_ddl>
    <remote_servers>
        <cluster>
            <shard>
                <internal_replication>true</internal_replication>
                <replica>
                    <host>clickhouse</host>
                    <port>9000</port>
                </replica>
            </shard>
        </cluster>
    </remote_servers>
</clickhouse>`

const clickHouseUserConfig = `<clickhouse>
    <profiles>
        <default>
            <enable_time_time64_type>1</enable_time_time64_type>
        </default>
    </profiles>
</clickhouse>`

const opikProxyConfig = `events {}
http {
    upstream backend {
        server backend:8080;
    }
    server {
        listen 5173;
        location = /health {
            access_log off;
            return 200 "healthy\n";
        }
        location /api/ {
            rewrite ^/api/(.*)$ /$1 break;
            proxy_pass http://backend;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
        }
    }
}`
