package worker

import (
	"context"
	"os"
	"testing"
	"time"
	"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
	"github.com/jackc/pgx/v5"
)

func TestSourceIdentityDatabase(t *testing.T) {
	dsn,connection:=os.Getenv("PACA_IDENTITY_TEST_DB"),os.Getenv("PACA_IDENTITY_TEST_CONNECTION")
	if dsn==""||connection=="" {t.Skip("official-host Action supplies the isolated database and connection")}
	ctx:=context.Background()
	cfg,err:=pgx.ParseConfig(dsn);if err!=nil{t.Fatal(err)}
	cfg.RuntimeParams["search_path"]=Schema
	db,err:=pgx.ConnectConfig(ctx,cfg);if err!=nil{t.Fatal(err)};defer db.Close(ctx)
	tx,err:=db.Begin(ctx);if err!=nil{t.Fatal(err)};defer tx.Rollback(ctx)
	path:="identity-"+time.Now().Format("150405.000000")+".md"
	note:=tasknotes.Task{Path:path,DateCreated:"2026-10-09T00:00:00Z",OccurrenceDate:"2026-10-09"}
	first,err:=linkSource(ctx,tx,connection,"11111111-1111-4111-8111-111111111111","test:period-one",note);if err!=nil{t.Fatal(err)}
	again,err:=linkSource(ctx,tx,connection,"11111111-1111-4111-8111-111111111111","test:period-one",note);if err!=nil||again!=first{t.Fatal("duplicate association changed identity",err)}
	other:=note;other.OccurrenceDate="2026-10-10"
	if _,err=linkSource(ctx,tx,connection,"22222222-2222-4222-8222-222222222222","test:period-two",other);err==nil{t.Fatal("active path silently rebound to another period")}
	if _,err=tx.Exec(ctx,"UPDATE sources SET state='deleted' WHERE id=$1",first);err!=nil{t.Fatal(err)}
	other.DateCreated="2026-10-10T00:00:00Z"
	next,err:=linkSource(ctx,tx,connection,"22222222-2222-4222-8222-222222222222","test:period-two",other);if err!=nil||next==first{t.Fatal("new birth must allocate a new source generation",err)}
	var generation int
	if err=tx.QueryRow(ctx,"SELECT generation FROM sources WHERE id=$1",next).Scan(&generation);err!=nil||generation!=2{t.Fatal("source generation uniqueness was bypassed",err)}
	if _,err=linkSource(ctx,tx,connection,"11111111-1111-4111-8111-111111111111","test:period-one",note);err==nil{t.Fatal("late old note must not steal the active path")}
}
