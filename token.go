package main

import (
	"errors"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
	vault "github.com/hashicorp/vault/api"
)

func (f *nomadFollower) manageTokens() {
	log := f.logger

	vaultTokenRenewed := make(chan string, 1)

	go func() {
		for vaultToken := range vaultTokenRenewed {
			f.vaultClient.SetToken(vaultToken)
		}
	}()

	for {
		if err := f.manageNomadToken(); err != nil {
			log.Println("Failed managing Nomad token: %w", err)
			time.Sleep(time.Minute)
		}
	}
}

func (f *nomadFollower) manageNomadToken() error {
	log := f.logger
	secret, err := f.vaultClient.Logical().Read("nomad/creds/nomad-follower")
	if err != nil {
		return err
	}

	if secretIdI, ok := secret.Data["secret_id"]; ok {
		if secretId, ok := secretIdI.(string); ok {
			f.getNomadClient().SetSecretID(secretId)
		} else {
			return errors.New("secret_id wasn't a string")
		}
	} else {
		return errors.New("nomad credentials didn't have secret_id")
	}

	input := &vault.LifetimeWatcherInput{Secret: secret}
	watcher, err := f.vaultClient.NewLifetimeWatcher(input)
	if err != nil {
		return err
	}

	go watcher.Start()
	defer watcher.Stop()

	for {
		select {
		case err := <-watcher.DoneCh():
			return err
		case renewal := <-watcher.RenewCh():
			log.Printf("Renewed nomad token: %#v", renewal)
		}
	}
}

func (f *nomadFollower) watchVaultToken(renewed chan string) {
	log := f.logger
	watcher, err := fsnotify.NewWatcher()
	die(log, err)
	defer watcher.Close()

	done := make(chan bool)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				log.Println("event:", event)
				if event.Op&fsnotify.Write == fsnotify.Write {
					log.Println("modified file:", event.Name)

					token, err := os.ReadFile(event.Name)
					die(log, err)
					renewed <- string(token)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()

	err = watcher.Add(f.vaultTokenFile)
	if err != nil {
		die(log, err)
	}
	<-done
}
