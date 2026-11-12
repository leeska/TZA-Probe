package ipguard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWorkerConnectLeaseIdentityBudgetAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := testStore(t)
	if _, err := store.SavePolicy(ctx, "node", testPolicy()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, "node", "google", "baseline", ""); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	broker := NewBroker(store)
	broker.Report("node", Status{Enabled: true})
	agentServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := broker.Accept(writer, request, request.Header.Get("X-Node"), strings.TrimPrefix(request.URL.Path, "/"), request.Header.Get("X-Stream-Secret")); err != nil {
			http.Error(writer, "denied", 403)
		}
	}))
	defer agentServer.Close()
	worker := httptest.NewServer(broker.WorkerHandler())
	defer worker.Close()
	connection, err := net.Dial("tcp", strings.TrimPrefix(worker.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(connection, "CONNECT /connect/%s HTTP/1.1\r\nHost: localhost\r\nX-Job-Lease: %s\r\nX-Destination: www.google.com\r\n\r\n", job.ID, job.Lease)
	offer, err := broker.Poll(ctx, "node")
	if err != nil || offer == nil {
		t.Fatalf("no offer: %v", err)
	}
	wrong := httptest.NewRecorder()
	if err := broker.Accept(wrong, httptest.NewRequest("GET", "/", nil), "other", offer.ID, offer.Secret); err == nil {
		t.Fatal("cross-node accepted")
	}
	header := http.Header{"X-Node": []string{"node"}, "X-Stream-Secret": []string{offer.Secret}}
	remote, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(agentServer.URL, "http")+"/"+offer.ID, header)
	if err != nil {
		t.Fatalf("upgrade %v, %v", err, response)
	}
	defer remote.Close()
	reader := bufio.NewReader(connection)
	response, err = http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("CONNECT failed: %v %v", response, err)
	}
	if err := remote.WriteMessage(websocket.BinaryMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 5)
	if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != "hello" {
		t.Fatalf("downstream %q %v", buffer, err)
	}
	if _, err := connection.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	_, data, err := remote.ReadMessage()
	if err != nil || string(data) != "world" {
		t.Fatalf("upstream %q %v", data, err)
	}
	if broker.Usage(job.ID) != 10 {
		t.Fatalf("unmetered: %d", broker.Usage(job.ID))
	}
	if err := broker.Accept(wrong, httptest.NewRequest("GET", "/", nil), "node", offer.ID, offer.Secret); err == nil {
		t.Fatal("offer replay accepted")
	}
	if err := store.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	remote.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := remote.ReadMessage(); err == nil {
		t.Fatal("cancel did not close stream")
	}
}

func TestWorkerRejectsMissingLeaseAndForbiddenDestination(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	broker := NewBroker(store)
	if _, err := store.SavePolicy(ctx, "node", testPolicy()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(ctx, "node", "google", "baseline", ""); err != nil {
		t.Fatal(err)
	}
	job, _ := store.Claim(ctx)
	for _, lease := range []string{"", job.Lease} {
		request := httptest.NewRequest("CONNECT", "http://localhost/connect/"+job.ID, nil)
		request.Header.Set("X-Job-Lease", lease)
		request.Header.Set("X-Destination", "127.0.0.1")
		writer := httptest.NewRecorder()
		broker.WorkerHandler().ServeHTTP(writer, request)
		if writer.Code < 400 {
			t.Fatalf("accepted forbidden CONNECT: %d", writer.Code)
		}
	}
}
