package golem

import (
	"github.com/pkg/errors"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/sandbox"
	"github.com/user/golem/pkg/tools"
	"github.com/user/golem/pkg/web"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// toolSet is every tool that can touch the host or the network, i.e. every tool
// the sandbox policy gates.
//
// It is built twice: once interactive for the user's own turn, and once
// unattended for subagents. A delegate runs with nobody watching it, so a call
// the policy would ask a human about must fail rather than wait - that is what
// stops a subagent approving on the user's behalf.
type toolSet struct {
	shell           tool.Tool
	readFile        tool.Tool
	writeFile       tool.Tool
	replaceRegex    tool.Tool
	findFiles       tool.Tool
	grepFiles       tool.Tool
	hashlineRead    tool.Tool
	hashlineFindBlk tool.Tool
	hashlinePatch   tool.Tool
	symbolOutline   tool.Tool
	webSearch       tool.Tool
	webFetch        tool.Tool
}

// newToolSet builds the host-touching tools for workDir under policy, in mode.
func newToolSet(workDir string, policy *sandbox.Policy, webCfg config.WebConfig, mode approval.Mode) (toolSet, error) {
	var ts toolSet
	opts := []tools.Option{tools.WithPolicy(policy)}
	webOpts := []web.Option{web.WithPolicy(policy)}
	if mode == approval.Unattended {
		opts = append(opts, tools.WithUnattended())
		webOpts = append(webOpts, web.WithUnattended())
	}

	var err error
	if ts.shell, err = tools.NewShellTool(opts...); err != nil {
		return ts, errors.Wrap(err, "creating shell tool")
	}
	if ts.readFile, err = tools.NewReadFileTool(workDir, opts...); err != nil {
		return ts, errors.Wrap(err, "creating read_file tool")
	}
	if ts.writeFile, err = tools.NewWriteFileTool(workDir, opts...); err != nil {
		return ts, errors.Wrap(err, "creating write_file tool")
	}
	if ts.replaceRegex, err = tools.NewReplaceRegexTool(workDir, opts...); err != nil {
		return ts, errors.Wrap(err, "creating replace_regex tool")
	}
	if ts.findFiles, err = tools.NewFindFilesTool(workDir, opts...); err != nil {
		return ts, errors.Wrap(err, "creating find_files tool")
	}
	if ts.grepFiles, err = tools.NewGrepFilesTool(workDir, opts...); err != nil {
		return ts, errors.Wrap(err, "creating grep_files tool")
	}
	if ts.hashlineRead, err = tools.NewHashlineReadTool(workDir, opts...); err != nil {
		return ts, errors.Wrap(err, "creating hashline_read tool")
	}
	if ts.hashlineFindBlk, err = tools.NewHashlineFindBlockTool(workDir, opts...); err != nil {
		return ts, errors.Wrap(err, "creating hashline_find_block tool")
	}
	if ts.hashlinePatch, err = tools.NewHashlinePatchTool(workDir, opts...); err != nil {
		return ts, errors.Wrap(err, "creating hashline_patch tool")
	}
	if ts.symbolOutline, err = tools.NewSymbolOutlineTool(workDir, opts...); err != nil {
		return ts, errors.Wrap(err, "creating symbol_outline tool")
	}
	searchProvider := web.NewGoogleCSE(webCfg.APIKey, webCfg.CX, webCfg.BaseURL)
	if ts.webSearch, err = web.NewWebSearchTool(searchProvider, webOpts...); err != nil {
		return ts, errors.Wrap(err, "creating web_search tool")
	}
	if ts.webFetch, err = web.NewWebFetchTool(web.NewFetcher(int64(webCfg.MaxFetch)), webOpts...); err != nil {
		return ts, errors.Wrap(err, "creating web_fetch tool")
	}
	return ts, nil
}

// gateMCPToolset puts MCP tools under the policy.
//
// MCP tools run in another process, so pkg/sandbox cannot inspect what they do:
// in policy and isolation mode every MCP tool call is a question unless the
// profile lists it in mcp_allow. A delegate has nobody to ask, so for the
// subagent set the same rule becomes a filter: only allowlisted MCP tools are
// offered at all, which keeps a delegate from doing through MCP what the policy
// would have stopped it doing directly.
func gateMCPToolset(ts tool.Toolset, policy *sandbox.Policy, delegate bool) tool.Toolset {
	if ts == nil {
		return nil
	}
	if policy == nil || policy.Mode() == sandbox.ModeOff {
		return ts
	}
	if delegate {
		return tool.FilterToolset(ts, func(_ agent.ReadonlyContext, t tool.Tool) bool {
			allowed := policy.MCPAllowed(t.Name())
			approval.Note(approval.Unattended, t.Name(), sandbox.Decision{
				Verdict: verdictFor(allowed),
				Rule:    "mcp-allow",
				Reason:  "MCP tools run outside the sandbox policy",
			}, "")
			return allowed
		})
	}
	return tool.WithConfirmation(ts, false, func(toolName string, _ any) bool {
		ask := policy.MCPRequiresConfirmation(toolName)
		verdict := sandbox.Allow
		rule := "mcp-allow"
		if ask {
			verdict = sandbox.Confirm
			rule = "mcp-confirm"
		}
		approval.Note(approval.Interactive, toolName, sandbox.Decision{
			Verdict: verdict,
			Rule:    rule,
			Reason:  "MCP tools run outside the sandbox policy",
		}, "")
		return ask
	})
}

func verdictFor(allowed bool) sandbox.Verdict {
	if allowed {
		return sandbox.Allow
	}
	return sandbox.Deny
}

// declaringTool is the part of a function tool that carries its schema. ADK
// keeps the interface unexported, so it is restated structurally here.
type declaringTool interface {
	Declaration() *genai.FunctionDeclaration
}

// toolDeclarations collects the function declarations a turn sends, so a
// compaction request can carry the same tool prefix (see LLMSummarizer.SetTools).
func toolDeclarations(tools []tool.Tool) []*genai.FunctionDeclaration {
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		dt, ok := t.(declaringTool)
		if !ok {
			continue
		}
		if d := dt.Declaration(); d != nil {
			decls = append(decls, d)
		}
	}
	return decls
}
