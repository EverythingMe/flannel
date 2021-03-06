// Copyright 2015 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"path"

	"github.com/coreos/flannel/Godeps/_workspace/src/golang.org/x/net/context"

	"github.com/coreos/flannel/subnet"
)

// implements subnet.Manager by sending requests to the server
type RemoteManager struct {
	base string // includes scheme, host, and port, and version
}

func NewRemoteManager(listenAddr string) subnet.Manager {
	return &RemoteManager{base: "http://" + listenAddr + "/v1"}
}

func (m *RemoteManager) mkurl(network string, parts ...string) string {
	if network == "" {
		network = "/_"
	}
	if network[0] != '/' {
		network = "/" + network
	}
	return m.base + path.Join(append([]string{network}, parts...)...)
}

func (m *RemoteManager) GetNetworkConfig(ctx context.Context, network string) (*subnet.Config, error) {
	url := m.mkurl(network, "config")

	resp, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}

	config := &subnet.Config{}
	if err := json.NewDecoder(resp.Body).Decode(config); err != nil {
		return nil, err
	}

	return config, nil
}

func (m *RemoteManager) AcquireLease(ctx context.Context, network string, attrs *subnet.LeaseAttrs) (*subnet.Lease, error) {
	url := m.mkurl(network, "leases/")

	body, err := json.Marshal(attrs)
	if err != nil {
		return nil, err
	}

	resp, err := httpPutPost(ctx, "POST", url, "application/json", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}

	newLease := &subnet.Lease{}
	if err := json.NewDecoder(resp.Body).Decode(newLease); err != nil {
		return nil, err
	}

	return newLease, nil
}

func (m *RemoteManager) RenewLease(ctx context.Context, network string, lease *subnet.Lease) error {
	url := m.mkurl(network, "leases", lease.Key())

	body, err := json.Marshal(lease)
	if err != nil {
		return err
	}

	resp, err := httpPutPost(ctx, "PUT", url, "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return httpError(resp)
	}

	newLease := &subnet.Lease{}
	if err := json.NewDecoder(resp.Body).Decode(newLease); err != nil {
		return err
	}

	*lease = *newLease
	return nil
}

func (m *RemoteManager) WatchLeases(ctx context.Context, network string, cursor interface{}) (subnet.WatchResult, error) {
	url := m.mkurl(network, "leases")

	if cursor != nil {
		c, ok := cursor.(string)
		if !ok {
			return subnet.WatchResult{}, fmt.Errorf("internal error: RemoteManager.WatchLeases received non-string cursor")
		}

		url = fmt.Sprintf("%v?next=%v", url, c)
	}

	resp, err := httpGet(ctx, url)
	if err != nil {
		return subnet.WatchResult{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return subnet.WatchResult{}, httpError(resp)
	}

	wr := subnet.WatchResult{}
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return subnet.WatchResult{}, err
	}
	if _, ok := wr.Cursor.(string); !ok {
		return subnet.WatchResult{}, fmt.Errorf("lease watch returned non-string cursor")
	}

	return wr, nil
}

func httpError(resp *http.Response) error {
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return fmt.Errorf("%v: %v", resp.Status, string(b))
}

type httpRespErr struct {
	resp *http.Response
	err  error
}

func httpDo(ctx context.Context, req *http.Request) (*http.Response, error) {
	// Run the HTTP request in a goroutine (so it can be canceled) and pass
	// the result via the channel c
	tr := &http.Transport{}
	client := &http.Client{Transport: tr}
	c := make(chan httpRespErr, 1)
	go func() {
		resp, err := client.Do(req)
		c <- httpRespErr{resp, err}
	}()

	select {
	case <-ctx.Done():
		tr.CancelRequest(req)
		<-c // Wait for f to return.
		return nil, ctx.Err()
	case r := <-c:
		return r.resp, r.err
	}
}

func httpGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	return httpDo(ctx, req)
}

func httpPutPost(ctx context.Context, method, url, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return httpDo(ctx, req)
}
