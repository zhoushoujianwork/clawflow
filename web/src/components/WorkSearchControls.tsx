import { CLEAR_WORK_SEARCH, WORK_STAGES, type WorkSearch } from '../lib/workSearch'
import type { ProjectEntry } from '../lib/projectMembership'
import { workRole, type ListItem, type ListRepo } from './WorkList'

export function WorkSearchControls({ search, repos, projects, projectError, update }: {
  search: WorkSearch
  repos: ListRepo<ListItem>[]
  projects: ProjectEntry[]
  projectError: boolean
  update: (next: WorkSearch & { work?: undefined; tab?: undefined }) => void
}) {
  const roles = [...new Set(repos.flatMap(r => r.items.map(workRole)))].sort()
  function select(label: string, key: 'workRepo' | 'project' | 'stage' | 'role', options: string[]) {
    const value = search[key] || ''
    return <label className="flex flex-col gap-1 min-w-0">{label}
      <select className="border rounded p-2 bg-card max-w-full" value={value} onChange={e => update({ [key]: e.target.value || undefined })}>
        <option value="">Any {label.toLowerCase()}</option>
        {value && !options.includes(value) && <option value={value}>{value} (unavailable)</option>}
        {options.map(option => <option key={option} value={option}>{option}</option>)}
      </select>
    </label>
  }
  return <section aria-label="Work filters" className="border rounded-xl p-3 mb-4 text-sm">
    <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
      <label className="flex flex-col gap-1">Title or issue
        <input type="search" className="border rounded p-2 bg-card min-w-0" value={search.q || ''} placeholder="Search title, #123 or repo#123" onChange={e => update({ q: e.target.value || undefined })} maxLength={256} />
      </label>
      {select('Repository', 'workRepo', repos.map(r => r.repo).sort())}
      {select('Project', 'project', projects.map(p => p.name).sort())}
      {select('Stage', 'stage', [...WORK_STAGES])}
      {select('Role', 'role', roles)}
      <label className="flex flex-col gap-1">Order
        <select className="border rounded p-2 bg-card" value={search.sort || 'queue'} onChange={e => update({ sort: e.target.value === 'queue' ? undefined : e.target.value as 'updated' | 'age' })}>
          <option value="queue">Dispatch order</option><option value="updated">Last update · newest first</option><option value="age">Age since last update · oldest first</option>
        </select>
      </label>
    </div>
    <button className="border rounded px-3 py-2 mt-3" onClick={() => update(CLEAR_WORK_SEARCH)}>Clear all filters</button>
    {projectError && <p role="status">Project membership unavailable; project-filtered results may be incomplete.</p>}
  </section>
}
