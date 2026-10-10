package main

// The bot-jobs tools on /mcp: list_bot_jobs, create_bot_job, update_bot_job,
// delete_bot_job and run_bot_job. Like the other bot tools they resolve the
// bot through botForCaller, so a bot out of the caller's reach reads as
// missing. A bot manages its own jobs with them when its human asks.

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const botJobScheduleHelp = "5-field cron (minute hour day-of-month month day-of-week) in `timezone`; several expressions may be joined with \";\" (e.g. \"47 7 * * *; 15 9 * * 1-5\"). Empty for no schedule (webhook or Run now only)."

type botJobsIn struct {
	Name string `json:"name" jsonschema:"the bot's name"`
}

type botJobsOut struct {
	Jobs    []botJobView   `json:"jobs"`
	Channel map[string]any `json:"channel" jsonschema:"whether the bot gets lasso's channel, which delivers its jobs, and whether it is listening now"`
}

type botJobOneIn struct {
	Name string `json:"name" jsonschema:"the bot's name"`
	Job  string `json:"job" jsonschema:"the job's name"`
}

type createBotJobIn struct {
	Name     string `json:"name" jsonschema:"the bot's name"`
	Job      string `json:"job" jsonschema:"the new job's name: lowercase letters, digits and dashes"`
	Message  string `json:"message,omitempty" jsonschema:"the instruction delivered to the bot each time the job fires"`
	Cron     string `json:"cron,omitempty" jsonschema:"the schedule"`
	Timezone string `json:"timezone,omitempty" jsonschema:"IANA time zone the schedule is read in, e.g. America/Los_Angeles; default UTC"`
	Webhook  bool   `json:"webhook,omitempty" jsonschema:"give the job a webhook URL; a POST's body is delivered after the message"`
	Paused   bool   `json:"paused,omitempty" jsonschema:"create it paused"`
}

type updateBotJobIn struct {
	Name     string  `json:"name" jsonschema:"the bot's name"`
	Job      string  `json:"job" jsonschema:"the job's current name"`
	Rename   *string `json:"rename,omitempty" jsonschema:"a new name for the job"`
	Message  *string `json:"message,omitempty"`
	Cron     *string `json:"cron,omitempty" jsonschema:"the schedule; empty removes it"`
	Timezone *string `json:"timezone,omitempty"`
	Webhook  *bool   `json:"webhook,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty" jsonschema:"false pauses the job: no schedule fires, webhooks are refused"`
}

type botJobOut struct {
	Job botJobView `json:"job"`
}

type runBotJobOut struct {
	EventID int64  `json:"event_id"`
	Status  string `json:"status" jsonschema:"pending (queued for the bot, merged with an undelivered run if there was one) or dropped (the bot is stopped)"`
}

func registerBotJobTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_bot_jobs",
		Description: "List a bot's jobs: scheduled prompts and webhooks lasso delivers into its session through its lasso-channel. Each has its schedule (cron and timezone), next fire, last delivery and webhook path and key (the URL is lasso's own origin + webhook_path + ?key=webhook_key).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botJobsIn) (*mcp.CallToolResult, botJobsOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botJobsOut{}, err
		}
		jobs, err := listBotJobs(rec.Name)
		if err != nil {
			return nil, botJobsOut{}, err
		}
		return nil, botJobsOut{Jobs: botJobViews(jobs), Channel: botChannelState(rec, time.Now())}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_bot_job",
		Description: "Create a job for a bot: a message lasso delivers into its session on a schedule, from a webhook, or when run by hand. Schedule: " + botJobScheduleHelp + " Takes effect at once, no restart.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createBotJobIn) (*mcp.CallToolResult, botJobOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botJobOut{}, err
		}
		enabled := !in.Paused
		j, err := createBotJob(rec, botJobInput{Name: &in.Job, Message: &in.Message, Cron: &in.Cron, Timezone: &in.Timezone, Webhook: &in.Webhook, Enabled: &enabled})
		if err != nil {
			return nil, botJobOut{}, err
		}
		return nil, botJobOut{Job: botJobViews([]*botJob{j})[0]}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_bot_job",
		Description: "Change a bot's job: only the fields you pass change. Schedule: " + botJobScheduleHelp,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateBotJobIn) (*mcp.CallToolResult, botJobOut, error) {
		j, err := botJobForCaller(req, in.Name, in.Job)
		if err != nil {
			return nil, botJobOut{}, err
		}
		if err := saveBotJob(j, botJobInput{Name: in.Rename, Message: in.Message, Cron: in.Cron, Timezone: in.Timezone, Webhook: in.Webhook, Enabled: in.Enabled}); err != nil {
			return nil, botJobOut{}, err
		}
		return nil, botJobOut{Job: botJobViews([]*botJob{j})[0]}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_bot_job",
		Description: "Delete a bot's job and its history.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botJobOneIn) (*mcp.CallToolResult, botChangeOut, error) {
		j, err := botJobForCaller(req, in.Name, in.Job)
		if err != nil {
			return nil, botChangeOut{}, err
		}
		if err := deleteBotJob(j); err != nil {
			return nil, botChangeOut{}, err
		}
		return nil, botChangeOut{OK: true}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "run_bot_job",
		Description: "Fire a bot's job now, as if its schedule came due (a paused job runs too). The event reaches the bot through its lasso-channel.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botJobOneIn) (*mcp.CallToolResult, runBotJobOut, error) {
		j, err := botJobForCaller(req, in.Name, in.Job)
		if err != nil {
			return nil, runBotJobOut{}, err
		}
		id, status, err := runBotJob(j)
		if err != nil {
			return nil, runBotJobOut{}, err
		}
		return nil, runBotJobOut{EventID: id, Status: status}, nil
	})
}

func botJobForCaller(req *mcp.CallToolRequest, bot, job string) (*botJob, error) {
	rec, _, err := botForCaller(req, bot)
	if err != nil {
		return nil, err
	}
	j, err := getBotJob(rec.Name, job)
	if err != nil {
		return nil, fmt.Errorf("%s has no job named %q", rec.Name, job)
	}
	return j, nil
}
