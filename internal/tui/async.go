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

// beginLoading is beginRequest plus the global spinner.
func (m *app) beginLoading(operation asyncOperation) asyncRequest {
	m.loading = true
	return m.beginRequest(operation)
}

// settleLoading stops the global spinner unless an unlock owns it.
func (m *app) settleLoading() {
	if !m.unlocking {
		m.loading = false
	}
}

// listingCurrent reports whether generation is the listing generation the
// list view currently targets, whether or not its rows have landed yet.
func (m app) listingCurrent(generation uint64) bool {
	return generation == m.generations[listOperation]
}

// currentRows reports whether state tagged with generation describes the rows
// on screen.
func (m app) currentRows(generation uint64) bool {
	return m.listLoaded && m.listingCurrent(generation)
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
