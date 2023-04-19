package adblock

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"

	"github.com/lib/pq"
	"github.ncsu.edu/jjuecks/vv8-post-processor/core"
)

type Script struct {
	info    *core.ScriptInfo
	blocked bool
}

func NewScript(info *core.ScriptInfo) *Script {
	return &Script{
		info: info,
	}
}

type flowAggregator struct {
	scriptList map[int]*Script
}

func NewAggregator() (core.Aggregator, error) {
	return &flowAggregator{
		scriptList: make(map[int]*Script),
	}, nil
}

func (agg *flowAggregator) IngestRecord(ctx *core.ExecutionContext, lineNumber int, op byte, fields []string) error {
	if (ctx.Script != nil) && !ctx.Script.VisibleV8 && (ctx.Origin != "") {
		_, ok := agg.scriptList[ctx.Script.ID]

		if !ok {
			agg.scriptList[ctx.Script.ID] = NewScript(ctx.Script)
		}
	}

	return nil
}

var scriptFlowFields = [...]string{
	"id",
	"isolate",
	"visiblev8",
	"code",
	"url",
	"evaled_by",
	"apis",
	"first_origin",
}

func (agg *flowAggregator) DumpToPostgresql(ctx *core.AggregationContext, sqlDb *sql.DB) error {

	txn, err := sqlDb.Begin()
	if err != nil {
		return err
	}

	stmt, err := txn.Prepare(pq.CopyIn("adblock", scriptFlowFields[:]...))
	if err != nil {
		txn.Rollback()
		return err
	}

	log.Printf("scriptFlow: %d scripts analysed", len(agg.scriptList))

	for _, script := range agg.scriptList {
		evaledBy := script.info.EvaledBy

		evaledById := -1
		if evaledBy != nil {
			evaledById = evaledBy.ID
		}

		_, err = stmt.Exec(
			script.info.ID,
			script.info.Isolate.ID,
			script.info.VisibleV8,
			script.info.Code,
			script.info.URL,
			evaledById,
			script.info.FirstOrigin)

		if err != nil {
			txn.Rollback()
			return err
		}

	}

	_, err = stmt.Exec()
	if err != nil {
		txn.Rollback()
	}
	err = stmt.Close()
	if err != nil {
		txn.Rollback()
		return err
	}
	err = txn.Commit()
	if err != nil {
		return err
	}

	return nil
}

func (agg *flowAggregator) DumpToStream(ctx *core.AggregationContext, stream io.Writer) error {
	jstream_output := json.NewEncoder(stream)

	adblockProc := exec.Command(os.Getenv("ADBLOCK_BINARY"))
	stdout, err := adblockProc.StdoutPipe()

	if err != nil {
		return err
	}

	stdin, err := adblockProc.StdinPipe()

	if err != nil {
		return err
	}

	err = adblockProc.Start()

	if err != nil {
		return err
	}

	jstreamAdblock := json.NewEncoder(stdin)

	for _, script := range agg.scriptList {
		jstreamAdblock.Encode(core.JSONObject{
			"url":    script.info.ID,
			"origin": script.info.FirstOrigin,
		})
	}

	buf := bufio.NewReader(stdout)

	for _, script := range agg.scriptList {
		line, _, _ := buf.ReadLine()
		if string(line) == "1" {
			script.blocked = true
		}
	}

	for _, script := range agg.scriptList {

		jstream_output.Encode(core.JSONArray{"adblock", core.JSONObject{
			"FirstOrigin": script.info.FirstOrigin,
			"URL":         script.info.URL,
			"Blocked":     script.blocked,
		}})
	}

	return nil
}
