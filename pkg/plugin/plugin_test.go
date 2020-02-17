/*
Copyright 2020 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	pb "k8s.io/apiserver/pkg/storage/value/encrypt/envelope/v1beta1"
	"sigs.k8s.io/aws-encryption-provider/pkg/cloud"
)

var (
	key              = "fakekey"
	encryptedMessage = "aGVsbG8gd29ybGQ="
	plainMessage     = "hello world"
	errorMessage     = fmt.Errorf("oops")
)

func TestEncrypt(t *testing.T) {
	tt := []struct {
		input  string
		ctx    map[string]string
		output string
		err    error
	}{
		{
			input:  plainMessage,
			ctx:    nil,
			output: encryptedMessage,
			err:    nil,
		},
		{
			input:  plainMessage,
			ctx:    nil,
			output: "",
			err:    errorMessage,
		},
		{
			input:  plainMessage,
			ctx:    make(map[string]string),
			output: encryptedMessage,
			err:    nil,
		},
		{
			input:  encryptedMessage,
			ctx:    map[string]string{"a": "b"},
			output: "",
			err:    errors.New("invalid context"),
		},
	}

	c := &cloud.KMSMock{}
	ctx := context.Background()

	for _, tc := range tt {
		func() {
			c.SetEncryptResp(tc.output, tc.err)
			p := New(key, c, nil)
			defer func() {
				p.stopCheckHealth()
			}()

			eReq := &pb.EncryptRequest{Plain: []byte(tc.input)}
			eRes, err := p.Encrypt(ctx, eReq)

			if tc.err != nil && err == nil {
				t.Fatalf("Failed to return expected error %v", tc.err)
			}

			if tc.err == nil && err != nil {
				t.Fatalf("Returned unexpected error: %v", err)
			}

			if tc.err == nil && string(eRes.Cipher) != StorageVersion+tc.output {
				t.Fatalf("Expected %s, but got %s", StorageVersion+tc.output, string(eRes.Cipher))
			}
		}()
	}
}
func TestDecrypt(t *testing.T) {
	tt := []struct {
		input  string
		ctx    map[string]string
		output string
		err    error
	}{
		{
			input:  encryptedMessage,
			ctx:    nil,
			output: plainMessage,
			err:    nil,
		},
		{
			input:  encryptedMessage,
			ctx:    nil,
			output: "",
			err:    errorMessage,
		},
		{
			input:  encryptedMessage,
			ctx:    map[string]string{"a": "b"},
			output: "",
			err:    errors.New("invalid context"),
		},
	}

	c := &cloud.KMSMock{}
	ctx := context.Background()

	for _, tc := range tt {
		func() {
			c.SetDecryptResp(tc.output, tc.err)
			p := New(key, c, tc.ctx)
			defer func() {
				p.stopCheckHealth()
			}()

			dReq := &pb.DecryptRequest{Cipher: []byte(tc.input)}
			dRes, err := p.Decrypt(ctx, dReq)

			if tc.err != nil && err == nil {
				t.Fatalf("Failed to return expected error %v", tc.err)
			}

			if tc.err == nil && err != nil {
				t.Fatalf("Returned unexpected error: %v", err)
			}

			if tc.err == nil && string(dRes.Plain) != tc.output {
				t.Fatalf("Expected %s, but got %s", tc.output, string(dRes.Plain))
			}
		}()
	}
}

func TestHealth(t *testing.T) {
	zap.ReplaceGlobals(zap.NewExample())

	tt := []struct {
		encryptErr error
		decryptErr error
	}{
		{
			encryptErr: nil,
			decryptErr: nil,
		},
		{
			encryptErr: errors.New("encrypt fail"),
			decryptErr: errors.New("decrypt fail"),
		},
	}
	for idx, entry := range tt {
		c := &cloud.KMSMock{}

		p := New(key, c, nil)
		defer func() {
			p.stopCheckHealth()
		}()

		c.SetEncryptResp("foo", entry.encryptErr)
		c.SetDecryptResp("foo", entry.decryptErr)

		_, encErr := p.Encrypt(context.Background(), &pb.EncryptRequest{Plain: []byte("foo")})
		if entry.encryptErr == nil && encErr != nil {
			t.Fatalf("#%d: unexpected error from Encrypt %v", idx, encErr)
		}
		herr1 := p.Health()
		if entry.encryptErr == nil {
			if herr1 != nil {
				t.Fatalf("#%d: unexpected error from Health %v", idx, herr1)
			}
		} else if !strings.HasSuffix(encErr.Error(), entry.encryptErr.Error()) {
			t.Fatalf("#%d: unexpected error from Health %v", idx, herr1)
		}

		_, decErr := p.Decrypt(context.Background(), &pb.DecryptRequest{Cipher: []byte("foo")})
		if entry.decryptErr == nil && decErr != nil {
			t.Fatalf("#%d: unexpected error from Encrypt %v", idx, decErr)
		}
		herr2 := p.Health()
		if entry.decryptErr == nil {
			if herr2 != nil {
				t.Fatalf("#%d: unexpected error from Health %v", idx, herr2)
			}
		} else if !strings.HasSuffix(decErr.Error(), entry.decryptErr.Error()) {
			t.Fatalf("#%d: unexpected error from Health %v", idx, herr2)
		}
	}
}

// TestHealthManyRequests sends many requests to fill up the error channel,
// and ensures following encrypt/decrypt operation do not block.
func TestHealthManyRequests(t *testing.T) {
	zap.ReplaceGlobals(zap.NewExample())

	c := &cloud.KMSMock{}

	p := newPlugin(key, c, nil, defaultHealthCheckPeriod, 0)
	defer func() {
		p.stopCheckHealth()
	}()

	c.SetEncryptResp("foo", errors.New("fail"))
	for i := 0; i < 10; i++ {
		errc := make(chan error)
		go func() {
			_, err := p.Encrypt(
				context.Background(),
				&pb.EncryptRequest{Plain: []byte("foo")},
			)
			errc <- err
		}()
		select {
		case <-time.After(time.Second):
			t.Fatalf("#%d: Encrypt took longer than it should", i)
		case err := <-errc:
			if !strings.HasSuffix(err.Error(), "fail") {
				t.Fatalf("#%d: unexpected errro %v", i, err)
			}
		}
	}
}
