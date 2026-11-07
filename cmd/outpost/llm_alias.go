package main

import "outpost/internal/llm"

// The Claude and OpenRouter clients live in internal/llm. These aliases keep
// the names the rest of package main already uses; the types are identical,
// so JSON and call sites do not change.

type (
	llmRequest   = llm.Request
	llmResponse  = llm.Response
	llmImage     = llm.Image
	agentTool    = llm.Tool
	toolCall     = llm.ToolCall
	agentMsg     = llm.Msg
	ToolDef      = llm.ToolDef
	WireToolCall = llm.WireToolCall
	fileBlock    = llm.FileBlock
	claudePrice  = llm.Price
	orModel      = llm.ORModel
	orCatalog    = llm.ORCatalog
)

const (
	imageTokenChars = llm.ImageTokenChars
	webSearchUSD    = llm.WebSearchUSD
	webSearchUses   = llm.WebSearchUses
	fileFormatHelp  = llm.FileFormatHelp
)

var (
	claudePrices       = llm.ClaudePrices
	claudeModelOrder   = llm.ClaudeModelOrder
	usageCost          = llm.UsageCost
	estimateCost       = llm.EstimateCost
	callClaude         = llm.CallClaude
	callOpenRouter     = llm.CallOpenRouter
	parseFileBlocks    = llm.ParseFileBlocks
	extractJSON        = llm.ExtractJSON
	openRouterEstimate = llm.OpenRouterEstimate
	openRouterLabel    = llm.OpenRouterLabel
	toolDefs           = llm.ToolDefs
	wireCalls          = llm.WireCalls
	fromWireCalls      = llm.FromWireCalls
)

// openRouterCatalog is the shared OpenRouter model list.
func openRouterCatalog() *llm.ORCatalog { return llm.OpenRouterModels }
