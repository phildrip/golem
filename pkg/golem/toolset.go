package golem

import (
	"github.com/pkg/errors"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/sandbox"
	"github.com/user/golem/pkg/tools"
	"github.com/user/golem/pkg/web"
	"google.golang.org/adk/v2/tool"
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
	searchProvider := web.NewGoogleCSE(webCfg.APIKey, webCfg.CX, webCfg.BaseURL)
	if ts.webSearch, err = web.NewWebSearchTool(searchProvider, webOpts...); err != nil {
		return ts, errors.Wrap(err, "creating web_search tool")
	}
	if ts.webFetch, err = web.NewWebFetchTool(web.NewFetcher(int64(webCfg.MaxFetch)), webOpts...); err != nil {
		return ts, errors.Wrap(err, "creating web_fetch tool")
	}
	return ts, nil
}
