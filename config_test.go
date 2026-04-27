package main

import (
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestLoadConfigFile(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		section       string
		wantUser      string
		wantPassword  string
		wantNet       string
		wantAddr      string
		wantErr       bool
	}{
		{
			name: "client section with user and password",
			content: `[client]
user = testuser
password = testpass
`,
			section:      "client",
			wantUser:     "testuser",
			wantPassword: "testpass",
			wantErr:      false,
		},
		{
			name: "client section with socket",
			content: `[client]
user = testuser
socket = /tmp/mysql.sock
`,
			section:  "client",
			wantUser: "testuser",
			wantNet:  "unix",
			wantAddr: "/tmp/mysql.sock",
			wantErr:  false,
		},
		{
			name: "client section with host",
			content: `[client]
user = testuser
host = 192.168.1.100
`,
			section:  "client",
			wantUser: "testuser",
			wantNet:  "tcp",
			wantAddr: "192.168.1.100",
			wantErr:  false,
		},
		{
			name: "custom section",
			content: `[mysql]
user = customuser
password = custompass
`,
			section:      "mysql",
			wantUser:     "customuser",
			wantPassword: "custompass",
			wantErr:      false,
		},
		{
			name: "missing section",
			content: `[client]
user = testuser
`,
			section: "nonexistent",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpfile, err := os.CreateTemp("", "mysql-config-*.cnf")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmpfile.Name())

			if _, err := tmpfile.Write([]byte(tt.content)); err != nil {
				t.Fatal(err)
			}
			if err := tmpfile.Close(); err != nil {
				t.Fatal(err)
			}

			config := &mysql.Config{}
			config, err = loadConfigFile(config, tmpfile.Name(), tt.section)

			if (err != nil) != tt.wantErr {
				t.Errorf("loadConfigFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if tt.wantUser != "" && config.User != tt.wantUser {
				t.Errorf("User = %v, want %v", config.User, tt.wantUser)
			}

			if tt.wantPassword != "" && config.Passwd != tt.wantPassword {
				t.Errorf("Passwd = %v, want %v", config.Passwd, tt.wantPassword)
			}

			if tt.wantNet != "" && config.Net != tt.wantNet {
				t.Errorf("Net = %v, want %v", config.Net, tt.wantNet)
			}

			if tt.wantAddr != "" && config.Addr != tt.wantAddr {
				t.Errorf("Addr = %v, want %v", config.Addr, tt.wantAddr)
			}
		})
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	config := &mysql.Config{}
	_, err := loadConfigFile(config, "/nonexistent/file.cnf", "client")
	if err == nil {
		t.Error("Expected error for nonexistent file, got nil")
	}
}

func TestLoadConfigFilePreservesExisting(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "mysql-config-*.cnf")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	content := `[client]
user = newuser
`
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	config := &mysql.Config{
		User:   "existinguser",
		Passwd: "existingpass",
	}

	config, err = loadConfigFile(config, tmpfile.Name(), "client")
	if err != nil {
		t.Fatal(err)
	}

	if config.User != "existinguser" {
		t.Errorf("User = %v, want existinguser (should preserve existing)", config.User)
	}

	if config.Passwd != "existingpass" {
		t.Errorf("Passwd = %v, want existingpass (should preserve existing)", config.Passwd)
	}
}
