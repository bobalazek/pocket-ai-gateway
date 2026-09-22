package gateway

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDispatchClientReleasesConnections(t *testing.T) {
	for _, protocol := range []string{"http1", "http2"} {
		for _, mode := range []string{"complete", "cancel_stream"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				closed := make(chan struct{}, 1)
				upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.WriteString(w, "data: hello\n\n")
					if mode == "cancel_stream" {
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					}
				}))
				upstream.EnableHTTP2 = protocol == "http2"
				upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateClosed {
						closed <- struct{}{}
					}
				}
				upstream.StartTLS()
				defer upstream.Close()
				client := safeClient(5*time.Second, true)
				defer client.CloseIdleConnections()
				client.Transport.(*http.Transport).TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if (response.ProtoMajor == 2) != upstream.EnableHTTP2 {
					t.Fatalf("unexpected protocol %s", response.Proto)
				}
				if mode == "cancel_stream" {
					if line, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil || line != "data: hello\n" {
						t.Fatalf("stream first event = %q, %v", line, err)
					}
					cancel()
				} else if body, err := io.ReadAll(response.Body); err != nil || string(body) != "data: hello\n\n" {
					t.Fatalf("complete response = %q, %v", body, err)
				}
				_ = response.Body.Close()
				select {
				case <-closed:
				case <-time.After(2 * time.Second):
					t.Fatal("single-dispatch client retained its connection after the response ended")
				}
			})
		}
	}
}
