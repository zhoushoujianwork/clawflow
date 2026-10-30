export function capacityFixtures(now = Date.now()) {
  const at = new Date(now).toISOString()
  const old = new Date(now - 180_000).toISOString()
  const worker = (name, extra = {}) => ({name,tags:[],roles:['operator'],live:true,last_seen:at,last_heartbeat:at,running_tasks:[],...extra})
  const task = (id, state) => ({id,state,stage:state,repo:'fixture/repo',issue:1,pr:0,kind:'implement',tags:[],priority:0,worker:'',lease_expires_at:null,attempt:0,application_attempts:0})
  const base = {generated_at:at,stale:false,refreshing:false,events:[],repos:[],sources:[],runtime:{version:'fixture',scheduler:{status:'idle',agent_slots_used:0,agent_slots_total:2},execution:{ready:true}},worker_pool:null}
  const local = {...base,sources:[{source:'execution',last_success:at,stale:false,refreshing:false}],repos:[{repo:'fixture/repo',enabled:true,items:[{issue:1,title:'Managed local work',stage:'implementing',evidence:[],why:'Fixture',executions:[{status:'running',machine:'local',provenance:{source:'offload_live',source_machine:'local',stale:false,observed_at:at,generated_at:at}}]}]}]}
  const pool = {generated_at:at,workers:[worker('remote')],tasks:[task('pending','pending'),task('leased','leased'),task('done','done')],throughput:{since:at,done:1,failed:0}}
  return {
    disabled:base,
    local,
    mixed:{...local,worker_pool:pool,repos:[{...local.repos[0],items:[...local.repos[0].items,{issue:2,title:'Remote recorded work',stage:'implementing',evidence:[],why:'Fixture',executions:[{status:'running',machine:'remote',provenance:{source:'offload_final',stale:false}}]}]}]},
    lost:{...base,worker_pool:{...pool,workers:[worker('lost',{live:false,last_seen:old,last_heartbeat:old}),worker('request-only',{last_heartbeat:old}),worker('idle',{last_heartbeat:null})]}},
    full:{...base,runtime:{...base.runtime,scheduler:{...base.runtime.scheduler,agent_slots_used:2}},worker_pool:pool},
    stale:{...local,stale:true,sources:[{source:'execution',last_success:old,stale:true}],worker_pool:{...pool,generated_at:old}},
    unknown:{...base,runtime:{...base.runtime,scheduler:{status:'idle'}}},
  }
}
