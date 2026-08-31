package tui

type asyncOperation uint8

const (
	listOperation asyncOperation = iota
	threadOperation
	previewOperation
	actionOperation
	labelOperation
	attachmentOperation
	openOperation
	profileOperation
	sendOperation
	unlockOperation
	composeOperation
	draftOperation
	asyncOperationCount
)

type asyncRequest struct {
	ctx               *accountCtx
	operation         asyncOperation
	generation        uint64
	listingGeneration uint64
}

type asyncMessage interface {
	requestRef() asyncRequest
}

func (m *app) beginRequest(operation asyncOperation) asyncRequest {
	m.generations[operation]++
	return m.currentRequest(operation)
}

func (m *app) invalidateRequests() {
	for operation := range m.generations {
		m.generations[operation]++
	}
}

func (m app) currentRequest(operation asyncOperation) asyncRequest {
	return asyncRequest{
		ctx:               m.ctx,
		operation:         operation,
		generation:        m.generations[operation],
		listingGeneration: m.generations[listOperation],
	}
}

func (m app) discardAsync(message asyncMessage) bool {
	request := message.requestRef()
	return request.ctx != m.ctx || m.generations[request.operation] != request.generation
}
