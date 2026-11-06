package app

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/RyutoHaraguchi/cloud-native-webhook-delivery/internal/core"
)

// recorder は偽物どうしをまたいだ呼び出しの順番を記録する。
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
}

// Calls は記録した呼び出しの名前を、呼ばれた順に返す。
func (r *recorder) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// Count は name の呼び出しの回数を返す。
func (r *recorder) Count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c == name {
			n++
		}
	}
	return n
}

// 記録する呼び出しの名前。
const (
	callGetMessage   = "Messages.GetMessage"
	callGetDelivery  = "Messages.GetDelivery"
	callSaveDelivery = "Messages.SaveDelivery"
	callSaveAttempt  = "Messages.SaveAttempt"
	callGetEndpoint  = "Endpoints.Get"
	callDisable      = "Endpoints.Disable"
	callDecrypt      = "Cipher.Decrypt"
	callSend         = "Queue.Send"
	callSendToDLQ    = "Queue.SendToDLQ"
	callReceive      = "Queue.Receive"
	callAck          = "Queue.Ack"
	callWebhook      = "Sender.Send"
)

type savedDelivery struct {
	Key        core.DeliveryKey
	Prev, Next core.Delivery
}

type savedAttempt struct {
	Key        core.DeliveryKey
	Prev, Next core.Delivery
	Attempt    core.Attempt
}

// fakeMessages は偽の MessageRepository。保存した値を持ち、エラーを差し替えられる。
// SaveDelivery と SaveAttempt は、差し替えたエラーがなければ保存値と prev を比べて条件付きで書く。
type fakeMessages struct {
	rec *recorder

	mu             sync.Mutex
	messages       map[[2]string]core.Message
	deliveries     map[core.DeliveryKey]core.Delivery
	getMessageErr  error
	getDeliveryErr error
	// blockGetDelivery なら GetDelivery は ctx が終わるまで待ち、ctx のエラーを返す（応答のない DynamoDB の代わり）
	blockGetDelivery bool
	saveDeliveryErr  error
	saveAttemptErr   error
	savedDeliveries  []savedDelivery
	savedAttempts    []savedAttempt
}

func newFakeMessages(rec *recorder) *fakeMessages {
	return &fakeMessages{
		rec:        rec,
		messages:   make(map[[2]string]core.Message),
		deliveries: make(map[core.DeliveryKey]core.Delivery),
	}
}

func (f *fakeMessages) PutMessage(m core.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[[2]string{m.TenantID, m.ID}] = m
}

func (f *fakeMessages) PutDelivery(key core.DeliveryKey, d core.Delivery) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries[key] = d
}

func (f *fakeMessages) CreateMessage(_ context.Context, m core.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := [2]string{m.TenantID, m.ID}
	if _, ok := f.messages[k]; ok {
		return core.ErrConflict
	}
	f.messages[k] = m
	return nil
}

func (f *fakeMessages) GetMessage(_ context.Context, tenantID, messageID string) (core.Message, error) {
	f.rec.record(callGetMessage)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getMessageErr != nil {
		return core.Message{}, f.getMessageErr
	}
	m, ok := f.messages[[2]string{tenantID, messageID}]
	if !ok {
		return core.Message{}, core.ErrNotFound
	}
	return m, nil
}

func (f *fakeMessages) CreateDelivery(_ context.Context, key core.DeliveryKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.deliveries[key]; !ok {
		f.deliveries[key] = core.NewDelivery()
	}
	return nil
}

func (f *fakeMessages) GetDelivery(ctx context.Context, key core.DeliveryKey) (core.Delivery, error) {
	f.rec.record(callGetDelivery)
	f.mu.Lock()
	if f.blockGetDelivery {
		f.mu.Unlock()
		<-ctx.Done()
		return core.Delivery{}, ctx.Err()
	}
	defer f.mu.Unlock()
	if f.getDeliveryErr != nil {
		return core.Delivery{}, f.getDeliveryErr
	}
	d, ok := f.deliveries[key]
	if !ok {
		return core.Delivery{}, core.ErrNotFound
	}
	return d, nil
}

func (f *fakeMessages) SaveDelivery(_ context.Context, key core.DeliveryKey, prev, next core.Delivery) error {
	f.rec.record(callSaveDelivery)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveDeliveryErr != nil {
		return f.saveDeliveryErr
	}
	if err := f.checkLocked(key, prev); err != nil {
		return err
	}
	f.deliveries[key] = next
	f.savedDeliveries = append(f.savedDeliveries, savedDelivery{Key: key, Prev: prev, Next: next})
	return nil
}

func (f *fakeMessages) SaveAttempt(_ context.Context, key core.DeliveryKey, prev, next core.Delivery, attempt core.Attempt) error {
	f.rec.record(callSaveAttempt)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveAttemptErr != nil {
		return f.saveAttemptErr
	}
	if err := f.checkLocked(key, prev); err != nil {
		return err
	}
	f.deliveries[key] = next
	f.savedAttempts = append(f.savedAttempts, savedAttempt{Key: key, Prev: prev, Next: next, Attempt: attempt})
	return nil
}

// checkLocked は保存値の Status と AttemptCount が prev と一致するかを確かめる。f.mu を持って呼ぶ。
func (f *fakeMessages) checkLocked(key core.DeliveryKey, prev core.Delivery) error {
	cur, ok := f.deliveries[key]
	if !ok || cur.Status() != prev.Status() || cur.AttemptCount() != prev.AttemptCount() {
		return core.ErrConflict
	}
	return nil
}

func (f *fakeMessages) SavedDeliveries() []savedDelivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.savedDeliveries)
}

func (f *fakeMessages) SavedAttempts() []savedAttempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.savedAttempts)
}

// fakeEndpoints は偽の EndpointRepository。
type fakeEndpoints struct {
	rec *recorder

	mu         sync.Mutex
	endpoints  map[[2]string]core.Endpoint
	getErr     error
	disableErr error
	disabled   [][2]string
}

func newFakeEndpoints(rec *recorder) *fakeEndpoints {
	return &fakeEndpoints{rec: rec, endpoints: make(map[[2]string]core.Endpoint)}
}

func (f *fakeEndpoints) Put(e core.Endpoint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endpoints[[2]string{e.TenantID, e.ID}] = e
}

func (f *fakeEndpoints) Get(_ context.Context, tenantID, endpointID string) (core.Endpoint, error) {
	f.rec.record(callGetEndpoint)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return core.Endpoint{}, f.getErr
	}
	e, ok := f.endpoints[[2]string{tenantID, endpointID}]
	if !ok {
		return core.Endpoint{}, core.ErrNotFound
	}
	return e, nil
}

func (f *fakeEndpoints) ListActive(_ context.Context, tenantID string) ([]core.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Endpoint
	for _, e := range f.endpoints {
		if e.TenantID == tenantID && e.Status == core.EndpointStatusActive {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeEndpoints) Disable(_ context.Context, tenantID, endpointID string) error {
	f.rec.record(callDisable)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disabled = append(f.disabled, [2]string{tenantID, endpointID})
	if f.disableErr != nil {
		return f.disableErr
	}
	k := [2]string{tenantID, endpointID}
	if e, ok := f.endpoints[k]; ok {
		e.Status = core.EndpointStatusDisabled
		f.endpoints[k] = e
	}
	return nil
}

func (f *fakeEndpoints) Disabled() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.disabled)
}

type decryptCall struct {
	SC         core.SecretContext
	Ciphertext []byte
}

// stubCipher は偽の SecretCipher。Decrypt は差し替えたエラーか plaintext の写しを返す。
// onDecrypt があれば復号中に呼ぶ（時計を進めるなど）。
type stubCipher struct {
	rec *recorder

	mu        sync.Mutex
	plaintext []byte
	err       error
	onDecrypt func()
	decrypts  []decryptCall
}

func (c *stubCipher) Encrypt(_ context.Context, _ core.SecretContext, plaintext []byte) ([]byte, error) {
	return append([]byte("enc:"), plaintext...), nil
}

func (c *stubCipher) Decrypt(_ context.Context, sc core.SecretContext, ciphertext []byte) ([]byte, error) {
	c.rec.record(callDecrypt)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.onDecrypt != nil {
		c.onDecrypt()
	}
	c.decrypts = append(c.decrypts, decryptCall{SC: sc, Ciphertext: slices.Clone(ciphertext)})
	if c.err != nil {
		return nil, c.err
	}
	return slices.Clone(c.plaintext), nil
}

func (c *stubCipher) Decrypts() []decryptCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.decrypts)
}

type sentDelivery struct {
	Key   core.DeliveryKey
	Delay time.Duration
}

// fakeQueue は偽の DeliveryPublisher と DeliveryConsumer。
// Receive は onReceive があれば何回目の呼び出しかを渡して最初に呼び、batches を先頭から 1 つずつ返す。尽きたらロングポーリングのように ctx の終わりまで待ち、ctx のエラーを返す。
type fakeQueue struct {
	rec *recorder

	mu         sync.Mutex
	sendErr    error
	dlqErr     error
	ackErr     error
	receiveErr error
	onReceive  func(call int)
	receives   int
	batches    [][]core.QueuedDelivery
	sent       []sentDelivery
	dlq        []core.DeliveryKey
	acks       []string
}

func (q *fakeQueue) Send(_ context.Context, key core.DeliveryKey, delay time.Duration) error {
	q.rec.record(callSend)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.sendErr != nil {
		return q.sendErr
	}
	q.sent = append(q.sent, sentDelivery{Key: key, Delay: delay})
	return nil
}

func (q *fakeQueue) SendToDLQ(_ context.Context, key core.DeliveryKey) error {
	q.rec.record(callSendToDLQ)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.dlqErr != nil {
		return q.dlqErr
	}
	q.dlq = append(q.dlq, key)
	return nil
}

func (q *fakeQueue) Receive(ctx context.Context, _ int) ([]core.QueuedDelivery, error) {
	q.rec.record(callReceive)
	q.mu.Lock()
	q.receives++
	call, onReceive := q.receives, q.onReceive
	q.mu.Unlock()
	if onReceive != nil {
		onReceive(call)
	}
	q.mu.Lock()
	if q.receiveErr != nil {
		q.mu.Unlock()
		return nil, q.receiveErr
	}
	if len(q.batches) == 0 {
		q.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	b := q.batches[0]
	q.batches = q.batches[1:]
	q.mu.Unlock()
	return b, nil
}

func (q *fakeQueue) Ack(_ context.Context, receipt string) error {
	q.rec.record(callAck)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ackErr != nil {
		return q.ackErr
	}
	q.acks = append(q.acks, receipt)
	return nil
}

func (q *fakeQueue) Sent() []sentDelivery {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.sent)
}

func (q *fakeQueue) DLQ() []core.DeliveryKey {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.dlq)
}

func (q *fakeQueue) Acks() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.acks)
}

// fakeSender は偽の WebhookSender。onSend があれば送信中に、Send に渡った ctx とリクエストで呼ぶ（時計を進めるなど）。
type fakeSender struct {
	rec *recorder

	mu       sync.Mutex
	resp     core.WebhookResponse
	err      error
	onSend   func(ctx context.Context, req core.WebhookRequest)
	requests []core.WebhookRequest
}

func (s *fakeSender) Send(ctx context.Context, req core.WebhookRequest) (core.WebhookResponse, error) {
	s.rec.record(callWebhook)
	s.mu.Lock()
	s.requests = append(s.requests, req)
	resp, err, onSend := s.resp, s.err, s.onSend
	s.mu.Unlock()
	if onSend != nil {
		onSend(ctx, req)
	}
	if err != nil {
		return core.WebhookResponse{}, err
	}
	return resp, nil
}

func (s *fakeSender) Requests() []core.WebhookRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}
