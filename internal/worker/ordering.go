package worker

import (
 "time"
 "github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
)

// No client timestamp is represented as a globally monotonic revision.
func eventDecision(s source,e tasknotes.Envelope,now time.Time)(string,string){
 stamp:=e.Version()
 if stamp.After(now.Add(5*time.Minute)){return "conflict","source clock is ahead; correct clock and resolve"}
 if s.State=="deleted" {
  if e.Event=="task.deleted" {return "duplicate",""}
  return "conflict","source tombstoned; a newer explicit creation is required"
 }
 base:=s.EventAt
 if base==nil { base=s.Version }
 if base!=nil && stamp.Before(*base) {return "stale","older event ignored"}
 if s.Snapshot!=nil && e.SnapshotHash()==s.Hash {return "duplicate",""}
 if base!=nil && stamp.Equal(*base) {
  if s.Snapshot==nil {return "conflict","legacy snapshot unavailable; verify association"}
  if e.Data.Previous!=nil && tasknotes.CanonicalHash(*e.Data.Previous,false)==tasknotes.CanonicalHash(*s.Snapshot,false){return "apply",""}
  t:=e.EffectiveTask()
  switch e.Event {
  case "task.archived","task.unarchived":
   if tasknotes.CanonicalHash(t,true)==tasknotes.CanonicalHash(*s.Snapshot,true){return "apply",""}
  case "task.deleted":
   if tasknotes.CanonicalHash(t,false)==tasknotes.CanonicalHash(*s.Snapshot,false){return "apply",""}
  case "task.completed":
   t.Status=s.Snapshot.Status
   if tasknotes.CanonicalHash(t,false)==tasknotes.CanonicalHash(*s.Snapshot,false){return "apply",""}
  }
  return "conflict","same event time differs without a verifiable predecessor"
 }
 if e.Data.Previous!=nil && s.Snapshot!=nil && tasknotes.CanonicalHash(*e.Data.Previous,false)!=tasknotes.CanonicalHash(*s.Snapshot,false) {
  return "conflict","previous snapshot differs from accepted source; resolve missed or concurrent edits"
 }
 return "apply",""
}
