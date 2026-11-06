package mqtt

import (
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// fakeToken implements paho.Token for unit tests.
type fakeToken struct {
	done chan struct{}
	err  error
}

func newSuccessToken() *fakeToken {
	c := make(chan struct{})
	close(c)
	return &fakeToken{done: c, err: nil}
}

func (t *fakeToken) Wait() bool                        { return true }
func (t *fakeToken) WaitTimeout(d time.Duration) bool  { return true }
func (t *fakeToken) Done() <-chan struct{}             { return t.done }
func (t *fakeToken) Error() error                      { return t.err }

// fakeMessage implements paho.Message.
type fakeMessage struct {
	topic    string
	payload  []byte
	retained bool
	qos      byte
}

func (m *fakeMessage) Duplicate() bool   { return false }
func (m *fakeMessage) Qos() byte         { return m.qos }
func (m *fakeMessage) Retained() bool    { return m.retained }
func (m *fakeMessage) Topic() string     { return m.topic }
func (m *fakeMessage) MessageID() uint16 { return 0 }
func (m *fakeMessage) Payload() []byte   { return m.payload }
func (m *fakeMessage) Ack()              {}

// PublishedMessage records an invocation of Publish for assertions.
type PublishedMessage struct {
	Topic    string
	QoS      byte
	Retained bool
	Payload  []byte
}

// FakeMQTTClient implements paho.Client in memory for tests.
type FakeMQTTClient struct {
	mu             sync.Mutex
	connected      bool
	subscriptions  map[string]paho.MessageHandler
	retained       map[string]fakeMessage
	published      []PublishedMessage
	unsubscribed   []string
	disconnectWait uint
}

func NewFakeMQTTClient() *FakeMQTTClient {
	return &FakeMQTTClient{
		connected:     true,
		subscriptions: make(map[string]paho.MessageHandler),
		retained:      make(map[string]fakeMessage),
		published:     make([]PublishedMessage, 0),
	}
}

func (c *FakeMQTTClient) IsConnected() bool      { return c.connected }
func (c *FakeMQTTClient) IsConnectionOpen() bool { return c.connected }

func (c *FakeMQTTClient) OptionsReader() paho.ClientOptionsReader {
	return paho.NewOptionsReader(paho.NewClientOptions())
}

func (c *FakeMQTTClient) Connect() paho.Token {
	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	return newSuccessToken()
}

func (c *FakeMQTTClient) Disconnect(quiesce uint) {
	c.mu.Lock()
	c.connected = false
	c.disconnectWait = quiesce
	c.mu.Unlock()
}

func (c *FakeMQTTClient) Publish(topic string, qos byte, retained bool, payload interface{}) paho.Token {
	c.mu.Lock()
	defer c.mu.Unlock()

	var data []byte
	switch p := payload.(type) {
	case []byte:
		data = make([]byte, len(p))
		copy(data, p)
	case string:
		data = []byte(p)
	}

	c.published = append(c.published, PublishedMessage{
		Topic:    topic,
		QoS:      qos,
		Retained: retained,
		Payload:  data,
	})

	if retained {
		if len(data) == 0 {
			delete(c.retained, topic)
		} else {
			c.retained[topic] = fakeMessage{
				topic:    topic,
				payload:  data,
				retained: true,
				qos:      qos,
			}
		}
	}
	return newSuccessToken()
}

// PublishedMessages returns a copy of all published messages recorded by the client.
func (c *FakeMQTTClient) PublishedMessages() []PublishedMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]PublishedMessage, len(c.published))
	copy(cp, c.published)
	return cp
}

func (c *FakeMQTTClient) Subscribe(topic string, qos byte, callback paho.MessageHandler) paho.Token {
	c.mu.Lock()
	c.subscriptions[topic] = callback
	var retainedToDeliver []fakeMessage
	for _, msg := range c.retained {
		if matchTopic(topic, msg.topic) {
			retainedToDeliver = append(retainedToDeliver, msg)
		}
	}
	c.mu.Unlock()

	for _, msg := range retainedToDeliver {
		m := msg
		callback(c, &m)
	}
	return newSuccessToken()
}

func (c *FakeMQTTClient) SubscribeMultiple(filters map[string]byte, callback paho.MessageHandler) paho.Token {
	c.mu.Lock()
	for filter := range filters {
		c.subscriptions[filter] = callback
	}
	var retainedToDeliver []fakeMessage
	for _, msg := range c.retained {
		for filter := range filters {
			if matchTopic(filter, msg.topic) {
				retainedToDeliver = append(retainedToDeliver, msg)
				break
			}
		}
	}
	c.mu.Unlock()

	for _, msg := range retainedToDeliver {
		m := msg
		callback(c, &m)
	}
	return newSuccessToken()
}

func (c *FakeMQTTClient) Unsubscribe(topics ...string) paho.Token {
	c.mu.Lock()
	c.unsubscribed = append(c.unsubscribed, topics...)
	for _, t := range topics {
		delete(c.subscriptions, t)
	}
	c.mu.Unlock()
	return newSuccessToken()
}

func (c *FakeMQTTClient) AddRoute(topic string, callback paho.MessageHandler) {
	c.mu.Lock()
	c.subscriptions[topic] = callback
	c.mu.Unlock()
}

func matchTopic(filter, topic string) bool {
	switch filter {
	case TopicMachinesStatus:
		return parseTopic(topic).typ == topicMachineStatus
	case TopicJobsStatus:
		return parseTopic(topic).typ == topicJobStatus
	case TopicJobsLogs:
		return parseTopic(topic).typ == topicJobLog
	case TopicAnnounceWildcard, TopicMachinesAnnounceWildcard:
		return parseTopic(topic).typ == topicAnnounce
	default:
		return filter == topic || filter == "#"
	}
}

// Deliver simulated MQTT message to all registered matching callbacks.
func (c *FakeMQTTClient) Deliver(topic string, payload []byte, retained bool) {
	c.mu.Lock()
	msg := fakeMessage{topic: topic, payload: payload, retained: retained}
	if retained {
		if len(payload) == 0 {
			delete(c.retained, topic)
		} else {
			c.retained[topic] = msg
		}
	}
	var handlers []paho.MessageHandler
	for filter, h := range c.subscriptions {
		if matchTopic(filter, topic) {
			handlers = append(handlers, h)
		}
	}
	c.mu.Unlock()

	for _, h := range handlers {
		m := msg
		h(c, &m)
	}
}
