// Package sshutil holds the SSH connection parameters shared by the SSH
// terminal and the SFTP file-transfer backends.
package sshutil

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// Config describes how to reach a remote host over SSH.
type Config struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password"`
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase"`
}

// Addr returns the host:port to dial, defaulting to port 22.
func (c Config) Addr() string {
	port := c.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(port))
}

// Validate reports whether the mandatory fields are present.
func (c Config) Validate() error {
	if c.Host == "" {
		return fmt.Errorf("请输入主机地址")
	}
	if c.User == "" {
		return fmt.Errorf("请输入用户名")
	}
	return nil
}

// Dial establishes an SSH connection. onHostKey (which may be nil) is called
// with the remote key type and SHA256 fingerprint. The host key is not
// verified: this is a debugging tool for trusted networks.
func Dial(cfg Config, onHostKey func(keyType, fingerprint string)) (*ssh.Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	var auth []ssh.AuthMethod
	if cfg.PrivateKey != "" {
		var (
			signer ssh.Signer
			err    error
		)
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		auth = append(auth, ssh.Password(cfg.Password))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("请提供密码或私钥")
	}

	clientCfg := &ssh.ClientConfig{
		User: cfg.User,
		Auth: auth,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			if onHostKey != nil {
				onHostKey(key.Type(), ssh.FingerprintSHA256(key))
			}
			return nil
		},
		Timeout: 12 * time.Second,
	}

	client, err := ssh.Dial("tcp", cfg.Addr(), clientCfg)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", cfg.Addr(), err)
	}
	return client, nil
}
