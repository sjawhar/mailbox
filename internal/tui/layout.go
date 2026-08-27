package tui

const (
	defaultTerminalWidth  = 80
	defaultTerminalHeight = 24
	defaultViewportHeight = 20
	searchPrompt          = "Search: "
	labelPrompt           = "Filter labels: "

	minimumInputWidth     = 20
	inputHorizontalChrome = 2
	minimumViewportWidth  = 20
	minimumViewportHeight = 5
	threadStatusRows      = 3

	splitPreviewMinWidth       = 110
	splitListMinWidth          = 52
	splitPreviewMinPaneWidth   = 30
	splitPaneGapWidth          = 1
	splitReservedHeight        = 6
	splitPaneHorizontalChrome  = 4
	splitContentHorizontalTrim = 6
	splitContentVerticalTrim   = 2
)

type layoutMetrics struct {
	width  int
	height int

	searchInputWidth int
	labelInputWidth  int
	readerWidth      int
	readerHeight     int
	searchListHeight int

	listPaneWidth       int
	previewPaneWidth    int
	splitPaneHeight     int
	listContentWidth    int
	previewContentWidth int
	splitContentHeight  int
}

func newLayoutMetrics(width, height int) layoutMetrics {
	listWidth := max(splitListMinWidth, width/2)
	previewWidth := max(splitPreviewMinPaneWidth, width-listWidth-splitPaneGapWidth)
	paneHeight := max(minimumViewportHeight, height-splitReservedHeight)

	return layoutMetrics{
		width:               width,
		height:              height,
		searchInputWidth:    max(minimumInputWidth, width-len(searchPrompt)-inputHorizontalChrome),
		labelInputWidth:     max(minimumInputWidth, width-len(labelPrompt)-inputHorizontalChrome),
		readerWidth:         max(minimumViewportWidth, width),
		readerHeight:        max(minimumViewportHeight, height-threadStatusRows),
		searchListHeight:    height - inputHorizontalChrome,
		listPaneWidth:       listWidth - splitPaneHorizontalChrome,
		previewPaneWidth:    previewWidth - splitPaneHorizontalChrome,
		splitPaneHeight:     paneHeight,
		listContentWidth:    max(1, listWidth-splitContentHorizontalTrim),
		previewContentWidth: max(minimumViewportWidth, previewWidth-splitContentHorizontalTrim),
		splitContentHeight:  max(1, paneHeight-splitContentVerticalTrim),
	}
}
