import { capacityFixtures } from './capacity-fixture.mjs'
export function activityFixture() {
  const board = structuredClone(capacityFixtures(Date.parse('2026-10-30T05:00:00Z')).local)
  const at = n => new Date(Date.parse('2026-10-30T00:00:00Z') + n * 1000).toISOString()
  const attempt = (id, start, end, status) => ({attempt_id:id,session_id:'shared-session',run_path:`runs/fixture__repo/issue-1/${id}`,status,started_at:at(start),ended_at:end ? at(end) : undefined,provenance:{source:'offload_final',stale:false,observed_at:at(30)}})
  board.repos[0].items[0].executions = [attempt('failed-attempt',1,2,'failed'),attempt('retry-attempt',3,4,'success'),attempt('stalled-attempt',5,0,'stalled')]
  Object.assign(board.repos[0].items[0],{pr:{number:10,state:'open',ci:'failed',updated_at:at(10)},acceptance:{verdict:'failed',completed_at:at(11)},delivery:{pr:10,merged_at:at(12)}})
  const event = (event,n,fields={}) => ({time:at(n),level:'info',event,fields:{repo:'fixture/repo',issue:'1',...fields}})
  board.events = [event('queue/skip',22),event('queue/skip',20),event('queue/skip',21),...Array.from({length:65},(_,i)=>event('run/harvest_end',100+i,{attempt:String(i)})),event('run/harvest_end',7,{repo:'other/repo',issue:'2'})]
  return board
}
