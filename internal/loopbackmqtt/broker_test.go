package loopbackmqtt

import (
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

func TestBroker_RetainedRoundTrip(t *testing.T) {
	b, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	pub := client(t, b.URL(), "pub")
	defer pub.Disconnect(100)
	token := pub.Publish("agentpulse/v1/machines/e2e-node/status", 1, true, []byte(`{"id":"e2e-node","online":true}`))
	if !token.WaitTimeout(3*time.Second) || token.Error() != nil {
		t.Fatalf("publish: %v", token.Error())
	}

	got := make(chan []byte, 1)
	sub := client(t, b.URL(), "sub")
	defer sub.Disconnect(100)
	subToken := sub.Subscribe("agentpulse/v1/machines/e2e-node/status", 1, func(_ paho.Client, msg paho.Message) {
		select {
		case got <- append([]byte(nil), msg.Payload()...):
		default:
		}
	})
	if !subToken.WaitTimeout(3*time.Second) || subToken.Error() != nil {
		t.Fatalf("subscribe: %v", subToken.Error())
	}
	select {
	case payload := <-got:
		if string(payload) != `{"id":"e2e-node","online":true}` {
			t.Fatalf("retained payload = %s", payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retained message was not delivered")
	}
}

func client(t *testing.T, url, id string) paho.Client {
	t.Helper()
	opts := paho.NewClientOptions().AddBroker(url).SetClientID(id).SetAutoReconnect(false)
	c := paho.NewClient(opts)
	token := c.Connect()
	if !token.WaitTimeout(3*time.Second) || token.Error() != nil {
		t.Fatalf("connect %s: %v", id, token.Error())
	}
	return c
}
