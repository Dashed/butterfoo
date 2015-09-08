package main

import (
    "encoding/json"
    "errors"
    "fmt"
    "strings"

    // 3rd-party
    "github.com/jmoiron/sqlx"
)

/* bootstrap */

// re. foreign_keys:
// > Foreign key constraints are disabled by default (for backwards compatibility),
// > so must be enabled separately for each database connection.
const BOOTSTRAP_QUERY string = `
PRAGMA foreign_keys=ON;
`

/* config table */
const SETUP_CONFIG_TABLE_QUERY string = `
CREATE TABLE IF NOT EXISTS Config (
    setting TEXT PRIMARY KEY NOT NULL,
    value TEXT,
    CHECK (setting <> '') /* ensure not empty */
);
`

// input: setting
var FETCH_CONFIG_SETTING_QUERY = (func() PipeInput {
    const __FETCH_CONFIG_SETTING_QUERY string = `
    SELECT setting, value FROM Config WHERE setting = :setting;
    `

    var requiredInputCols []string = []string{"setting"}

    return composePipes(
        MakeCtxMaker(__FETCH_CONFIG_SETTING_QUERY),
        EnsureInputColsPipe(requiredInputCols),
        BuildQueryPipe,
    )
}())

// input: setting, value
var SET_CONFIG_SETTING_QUERY = (func() PipeInput {
    const __INSERT_CONFIG_SETTING_QUERY string = `
    INSERT OR REPLACE INTO Config(setting, value) VALUES (:setting, :value);
    `

    var requiredInputCols []string = []string{"setting", "value"}

    return composePipes(
        MakeCtxMaker(__INSERT_CONFIG_SETTING_QUERY),
        EnsureInputColsPipe(requiredInputCols),
        BuildQueryPipe,
    )
}())

/* decks table */
const SETUP_DECKS_TABLE_QUERY string = `
CREATE TABLE IF NOT EXISTS Decks (
    deck_id INTEGER PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    CHECK (name <> '') /* ensure not empty */
);

/* closure table and associated triggers for Decks */

CREATE TABLE IF NOT EXISTS DecksClosure (
    ancestor INTEGER NOT NULL,
    descendent INTEGER NOT NULL,
    depth INTEGER NOT NULL,
    PRIMARY KEY(ancestor, descendent),
    FOREIGN KEY (ancestor) REFERENCES Decks(deck_id) ON DELETE CASCADE,
    FOREIGN KEY (descendent) REFERENCES Decks(deck_id) ON DELETE CASCADE
);

CREATE TRIGGER IF NOT EXISTS decks_closure_new_deck AFTER INSERT
ON Decks
BEGIN
    INSERT OR IGNORE INTO DecksClosure(ancestor, descendent, depth) VALUES (NEW.deck_id, NEW.deck_id, 0);
END;
`

var CREATE_NEW_DECK_QUERY = (func() PipeInput {
    const __CREATE_NEW_DECK_QUERY string = `
    INSERT INTO Decks(name) VALUES (:name);
    `
    var requiredInputCols []string = []string{"name"}

    return composePipes(
        MakeCtxMaker(__CREATE_NEW_DECK_QUERY),
        EnsureInputColsPipe(requiredInputCols),
        BuildQueryPipe,
    )
}())

var FETCH_DECK_QUERY = (func() PipeInput {
    const __FETCH_DECK_QUERY string = `
    SELECT deck_id, name FROM Decks WHERE deck_id = :deck_id;
    `

    var requiredInputCols []string = []string{"deck_id"}

    return composePipes(
        MakeCtxMaker(__FETCH_DECK_QUERY),
        EnsureInputColsPipe(requiredInputCols),
        BuildQueryPipe,
    )
}())

var UPDATE_DECK_QUERY = (func() PipeInput {
    const __UPDATE_DECK_QUERY string = `
    UPDATE Decks
    SET
    %s
    WHERE deck_id = :deck_id
    `

    var requiredInputCols []string = []string{"deck_id"}
    var whiteListCols []string = []string{"name"}

    return composePipes(
        MakeCtxMaker(__UPDATE_DECK_QUERY),
        EnsureInputColsPipe(requiredInputCols),
        PatchFilterPipe(whiteListCols),
        BuildQueryPipe,
    )
}())

// decks closure queries

// params:
// ?1 := parent
// ?2 := child
var ASSOCIATE_DECK_AS_CHILD_QUERY = (func() PipeInput {
    const __ASSOCIATE_DECK_AS_CHILD_QUERY string = `
    INSERT OR IGNORE INTO DecksClosure(ancestor, descendent, depth)

    /* for every ancestor of parent, make it an ancestor of child */
    SELECT t.ancestor, :child, t.depth+1
    FROM DecksClosure AS t
    WHERE t.descendent = :parent

    UNION ALL

    /* child is an ancestor of itself with a depth of 0 */
    SELECT :child, :child, 0;
    `

    var requiredInputCols []string = []string{"parent", "child"}

    return composePipes(
        MakeCtxMaker(__ASSOCIATE_DECK_AS_CHILD_QUERY),
        EnsureInputColsPipe(requiredInputCols),
        BuildQueryPipe,
    )
}())

// fetch direct children
var DECK_CHILDREN_QUERY = (func() PipeInput {
    const __DECK_CHILDREN_QUERY string = `
    SELECT ancestor, descendent, depth
    FROM DecksClosure
    WHERE
    ancestor = :parent
    AND depth = 1;
    `

    var requiredInputCols []string = []string{"parent"}

    return composePipes(
        MakeCtxMaker(__DECK_CHILDREN_QUERY),
        EnsureInputColsPipe(requiredInputCols),
        BuildQueryPipe,
    )
}())

/* helpers */

func JSON2Map(rawJSON []byte) (*StringMap, error) {

    var newMap StringMap

    err := json.Unmarshal(rawJSON, &newMap)
    if err != nil {
        return nil, err
    }

    return &newMap, nil
}

type StringMap map[string]interface{}

type QueryContext struct {
    query    string
    nameArgs *StringMap
    args     []interface{}
}

func MakeCtxMaker(baseQuery string) func() *QueryContext {
    return func() *QueryContext {
        var ctx QueryContext
        ctx.query = baseQuery
        ctx.nameArgs = &(StringMap{})

        return &ctx
    }
}

type PipeInput func(...interface{}) (*QueryContext, PipeInput, error)
type Pipe func(*QueryContext, *([]Pipe)) PipeInput

// TODO: rename to waterfallPipes; since this isn't really an actual compose operation
func composePipes(makeCtx func() *QueryContext, pipes ...Pipe) PipeInput {

    if len(pipes) <= 0 {
        return func(args ...interface{}) (*QueryContext, PipeInput, error) {
            return nil, nil, nil
            // noop
        }
    }

    var firstPipe Pipe = pipes[0]
    var restPipes []Pipe = pipes[1:]
    return func(args ...interface{}) (*QueryContext, PipeInput, error) {
        return firstPipe(makeCtx(), &restPipes)(args...)
    }
}

func EnsureInputColsPipe(required []string) Pipe {
    return func(ctx *QueryContext, pipes *([]Pipe)) PipeInput {
        return func(args ...interface{}) (*QueryContext, PipeInput, error) {

            var (
                inputMap *StringMap = args[0].(*StringMap)
                nameArgs *StringMap = (*ctx).nameArgs
            )

            for _, col := range required {
                value, exists := (*inputMap)[col]

                if !exists {
                    return nil, nil, errors.New(fmt.Sprintf("missing required col: %s\nfor query: %s", col, ctx.query))
                }

                (*nameArgs)[col] = value
            }

            nextPipe := (*pipes)[0]
            restPipes := (*pipes)[1:]

            return ctx, nextPipe(ctx, &restPipes), nil
        }
    }
}

// given whitelist of cols and an unmarshaled json map, construct update query fragment
// for updating value of cols
func PatchFilterPipe(whitelist []string) Pipe {
    return func(ctx *QueryContext, pipes *([]Pipe)) PipeInput {
        return func(args ...interface{}) (*QueryContext, PipeInput, error) {

            var (
                patch           *StringMap = args[0].(*StringMap)
                namedSetStrings []string   = make([]string, 0, len(whitelist))
                nameArgs        *StringMap = (*ctx).nameArgs
                patched         bool       = false
            )

            for _, col := range whitelist {
                value, exists := (*patch)[col]

                if exists {
                    (*nameArgs)[col] = value
                    patched = true

                    setstring := fmt.Sprintf("%s = :%s", col, col)
                    namedSetStrings = append(namedSetStrings, setstring)
                }
            }

            if !patched {
                return nil, nil, errors.New("nothing patched")
            }

            (*ctx).query = fmt.Sprintf((*ctx).query, strings.Join(namedSetStrings, ", "))

            nextPipe := (*pipes)[0]
            restPipes := (*pipes)[1:]

            return ctx, nextPipe(ctx, &restPipes), nil
        }
    }
}

func BuildQueryPipe(ctx *QueryContext, _ *([]Pipe)) PipeInput {
    return func(args ...interface{}) (*QueryContext, PipeInput, error) {

        // this apparently doesn't work
        // var nameArgs StringMap = *((*ctx).nameArgs)
        var nameArgs map[string]interface{} = *((*ctx).nameArgs)

        query, args, err := sqlx.Named((*ctx).query, nameArgs)

        if err != nil {
            return nil, nil, err
        }

        ctx.query = query
        ctx.args = args

        return ctx, nil, nil
    }
}

func QueryApply(pipe PipeInput, stringmaps ...*StringMap) (string, []interface{}, error) {

    var (
        err         error
        currentPipe PipeInput = pipe
        ctx         *QueryContext
        idx         int = 0
    )

    for currentPipe != nil {

        var args []interface{} = []interface{}{}

        if idx < len(stringmaps) {
            args = append(args, stringmaps[idx])
            idx++
        }

        ctx, currentPipe, err = currentPipe(args...)
        if err != nil {
            return "", nil, err
        }
    }

    if ctx != nil {
        return ctx.query, ctx.args, nil
    }

    return "", nil, nil
}
