package storage

import (
	"context"
	"errors"
	"testing"
)

func TestNormalizeBYO(t *testing.T) {
	cases := []struct {
		name    string
		in      BYOInput
		wantErr bool
	}{
		{"ok", BYOInput{AccountID: "a", Endpoint: "https://s3.x", Bucket: "b", AccessKey: "k", SecretKey: "s"}, false},
		{"no account", BYOInput{Endpoint: "https://s3.x", Bucket: "b", AccessKey: "k", SecretKey: "s"}, true},
		{"http endpoint", BYOInput{AccountID: "a", Endpoint: "http://s3.x", Bucket: "b", AccessKey: "k", SecretKey: "s"}, true},
		{"no bucket", BYOInput{AccountID: "a", Endpoint: "https://s3.x", AccessKey: "k", SecretKey: "s"}, true},
		{"no creds", BYOInput{AccountID: "a", Endpoint: "https://s3.x", Bucket: "b"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := normalizeBYO(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !cfg.BYO {
				t.Error("expected BYO=true")
			}
			if cfg.Region != "auto" {
				t.Errorf("expected default region auto, got %q", cfg.Region)
			}
		})
	}
}

func TestConnectBYO_ValidatesLiveThenStores(t *testing.T) {
	mem := NewMemProvider()
	if err := mem.EnsureBucket(context.Background(), "my-bucket"); err != nil {
		t.Fatal(err)
	}
	restore := SetBYOProviderFactoryForTest(func(_ Config) (Provider, error) { return mem, nil })
	defer restore()

	svc := &Service{Store: NewMemStore(), ProviderForAccount: func(_ context.Context, _ string) (Provider, error) { return mem, nil }}

	cfg, err := svc.ConnectBYO(context.Background(), BYOInput{
		AccountID: "acct", Endpoint: "https://s3.x", Bucket: "my-bucket", AccessKey: "k", SecretKey: "s",
	})
	if err != nil {
		t.Fatalf("ConnectBYO: %v", err)
	}
	if cfg.SecretKey != "" {
		t.Error("returned config must not include secret")
	}
	stored, _ := svc.Store.GetConfig(context.Background(), "acct")
	if !stored.BYO || stored.Bucket != "my-bucket" {
		t.Fatalf("stored config wrong: %+v", stored)
	}
}

func TestConnectBYO_FailsWhenBucketUnreachable(t *testing.T) {
	mem := NewMemProvider() // bucket not created → ListBucket errors
	restore := SetBYOProviderFactoryForTest(func(_ Config) (Provider, error) { return mem, nil })
	defer restore()

	svc := &Service{Store: NewMemStore()}
	_, err := svc.ConnectBYO(context.Background(), BYOInput{
		AccountID: "acct", Endpoint: "https://s3.x", Bucket: "missing", AccessKey: "k", SecretKey: "s",
	})
	if !errors.Is(err, ErrBYOValidation) {
		t.Fatalf("expected ErrBYOValidation, got %v", err)
	}
	if _, err := svc.Store.GetConfig(context.Background(), "acct"); err == nil {
		t.Fatal("config must not be stored when validation fails")
	}
}

func TestDisconnectBYO(t *testing.T) {
	svc := &Service{Store: NewMemStore()}
	_ = svc.Store.PutConfig(context.Background(), Config{AccountID: "acct", BYO: true, Bucket: "b"})
	if err := svc.DisconnectBYO(context.Background(), "acct"); err != nil {
		t.Fatalf("DisconnectBYO: %v", err)
	}
	if _, err := svc.Store.GetConfig(context.Background(), "acct"); err == nil {
		t.Fatal("expected config removed")
	}
}
