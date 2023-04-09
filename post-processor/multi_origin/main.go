package multiorigin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"github.ncsu.edu/jjuecks/vv8-post-processor/core"
	"github.ncsu.edu/jjuecks/vv8-post-processor/features"
)

type Object struct {
	ID      int
	Origins map[string][]string
	URLs    map[string]bool
}

func NewObject(ID int) *Object {
	return &Object{
		ID:      ID,
		Origins: make(map[string][]string, 0),
		URLs:    make(map[string]bool, 0),
	}
}

type multioriginAggregator struct {
	objectList map[int]*Object
}

func NewAggregator() (core.Aggregator, error) {
	return &multioriginAggregator{
		objectList: make(map[int]*Object),
	}, nil
}

func (agg *multioriginAggregator) IngestRecord(ctx *core.ExecutionContext, lineNumber int, op byte, fields []string) error {
	if (ctx.Script != nil) && !ctx.Script.VisibleV8 && (ctx.Origin != "") {
		var receiver, member string
		switch op {
		case 'g', 's':
			receiver, _ = core.StripCurlies(fields[1])
			member, _ = core.StripQuotes(fields[2])
		case 'n':
			receiver, _ = core.StripCurlies(fields[1])
			receiver = strings.TrimPrefix(receiver, "%")
		case 'c':
			receiver, _ = core.StripCurlies(fields[2])
			member, _ = core.StripQuotes(fields[1])

			member = strings.TrimPrefix(member, "%")
		default:
			return fmt.Errorf("%d: invalid mode '%c'; fields: %v", lineNumber, op, fields)
		}

		if features.FilterName(member) {
			// We have some names (V8 special cases, numeric indices) that are never useful
			return nil
		}

		objectIDStr := ""

		if strings.Contains(receiver, ",") {
			objectIDStr = strings.Split(receiver, ",")[0]
			receiver = strings.Split(receiver, ",")[1]
		}

		objectID, err := strconv.Atoi(objectIDStr)
		if err != nil {
			// Ignore invalid Object IDs
			return nil
		}

		var fullName string
		if member != "" {
			fullName = fmt.Sprintf("%s.%s", receiver, member)
		} else {
			fullName = receiver
		}

		object, ok := agg.objectList[objectID]

		if !ok {
			object = NewObject(objectID)
			agg.objectList[objectID] = object
		}

		object.Origins[ctx.Origin] = append(object.Origins[ctx.Origin], fullName)
		object.URLs[ctx.Script.URL] = true
	}

	return nil
}

var multioriginObjectFields = [...]string{
	"objectid",
	"origins",
	"num_of_origins",
	"urls",
}

var multioriginApiNameFields = [...]string{
	"objectid",
	"origin",
	"api_name",
}

func (agg *multioriginAggregator) DumpToPostgresql(ctx *core.AggregationContext, sqlDb *sql.DB) error {

	txn, err := sqlDb.Begin()
	if err != nil {
		return err
	}

	stmtObj, err := txn.Prepare(pq.CopyIn("multi_origin_obj", multioriginObjectFields[:]...))
	if err != nil {
		txn.Rollback()
		return err
	}

	for _, object := range agg.objectList {
		allOrigins := make([]string, 0, len(object.Origins))

		for origin := range object.Origins {
			allOrigins = append(allOrigins, origin)
		}

		allUrls := make([]string, 0, len(object.URLs))

		for urls := range object.URLs {
			allUrls = append(allUrls, urls)
		}

		if len(allOrigins) > 1 {
			_, err = stmtObj.Exec(
				object.ID,
				pq.Array(allOrigins),
				len(allOrigins),
				pq.Array(allUrls))

			if err != nil {
				txn.Rollback()
				return err
			}

		}
	}

	_, err = stmtObj.Exec()
	if err != nil {
		txn.Rollback()
	}
	err = stmtObj.Close()
	if err != nil {
		txn.Rollback()
		return err
	}

	stmtApi, err := txn.Prepare(pq.CopyIn("multi_origin_api_names", multioriginApiNameFields[:]...))
	if err != nil {
		txn.Rollback()
		return err
	}

	for _, object := range agg.objectList {
		allOrigins := make([]string, 0, len(object.Origins))

		for origin := range object.Origins {
			allOrigins = append(allOrigins, origin)
		}

		if len(allOrigins) > 1 {
			for origin, apiNames := range object.Origins {
				for _, apiName := range apiNames {
					_, err = stmtApi.Exec(
						object.ID,
						origin,
						apiName)

					if err != nil {
						txn.Rollback()
						return err
					}
				}
			}
		}
	}

	_, err = stmtApi.Exec()
	if err != nil {
		txn.Rollback()
	}
	err = stmtApi.Close()
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

func (agg *multioriginAggregator) DumpToStream(ctx *core.AggregationContext, stream io.Writer) error {
	jstream := json.NewEncoder(stream)

	for _, object := range agg.objectList {
		allOrigins := make([]string, 0, len(object.Origins))

		for origin := range object.Origins {
			allOrigins = append(allOrigins, origin)
		}

		allUrls := make([]string, 0, len(object.URLs))

		for urls := range object.URLs {
			allUrls = append(allUrls, urls)
		}

		if len(allOrigins) > 1 {
			jstream.Encode(core.JSONArray{"multiorigin", core.JSONObject{
				"ID":      object.ID,
				"Origins": object.Origins,
				"URLs":    allUrls,
			}})
		}
	}
	return nil
}
