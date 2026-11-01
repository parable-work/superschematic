package behaviorops

import (
	ir "github.com/parable-work/superschematic/ir"
)

// ExpandWorkers makes each @behaviorWorker of api an ordinary worker of the
// queues its table's behaviors keep (D103): its Queue is the deadline queue
// the database's load added for the table (ir.TypeDef.BehaviorQueue), and
// its Origin ir.OriginBehavior. From there it is a worker as D53 has it: a
// deployable of kind worker named after the API and the class, its
// settings, its process under `stack dev`, its image and its worker pool;
// only its handler is the API's generator's, which runs each claimed
// message through the behaviors' core. It assumes the workers passed
// verify.CheckBehaviorWorkers, so db, the API's database, holds each
// queue, and it may run again on its own output.
func ExpandWorkers(api, db *ir.Schema) {
	if api == nil || db == nil {
		return
	}
	for _, worker := range api.Workers {
		if worker == nil || worker.BehaviorWorker == nil {
			continue
		}
		if queue := db.BehaviorQueueOf(worker.BehaviorWorker.Type, ir.BehaviorQueueDeadlines); queue != nil {
			worker.Queue = queue.Name
			worker.Origin = ir.OriginBehavior
		}
	}
}
