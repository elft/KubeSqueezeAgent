import { useEffect, useMemo, useState } from 'react'
import { QueryClient, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ColumnDef, flexRender, getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { deleteJSON, getJSON, postJSON, streamPolicyDraft } from './api'
import type { AuditEvent, ChartPoint, Execution, LLMStatus, PolicyDraft, PolicyVersion, Recommendation, Summary, Workload } from './types'

type Tab = 'overview' | 'workloads' | 'recommendations' | 'policies' | 'executions' | 'audit'

export function App({ queryClient }: { queryClient: QueryClient }) {
  const [tab, setTab] = useState<Tab>('overview')
  const [error, setError] = useState('')
  const summary = useQuery({ queryKey: ['summary'], queryFn: () => getJSON<Summary>('/api/v1/summary') })

  useEffect(() => {
    const source = new EventSource('/api/v1/events')
    source.addEventListener('audit', () => {
      void queryClient.invalidateQueries()
    })
    return () => source.close()
  }, [queryClient])

  const analyze = useMutation({
    mutationFn: () => postJSON('/api/v1/analyze'),
    onSuccess: () => { setError(''); void queryClient.invalidateQueries() },
    onError: (value: Error) => setError(value.message),
  })

  return (
    <div className="shell">
      <header className="topbar">
        <div className="brand"><span className="brandmark">KS</span><div><strong>KubeSqueeze</strong><small>Safe Kubernetes consolidation</small></div></div>
        <div className="cluster-pill"><span className={summary.data?.clusterStatus === 'connected' ? 'dot online' : 'dot'} />{summary.data?.clusterName ?? 'Connecting…'}</div>
        <button className="primary" onClick={() => analyze.mutate()} disabled={analyze.isPending}>{analyze.isPending ? 'Queuing…' : 'Run analysis'}</button>
      </header>
      <nav className="tabs">
        {(['overview', 'workloads', 'recommendations', 'policies', 'executions', 'audit'] as Tab[]).map((item) => (
          <button key={item} className={tab === item ? 'active' : ''} onClick={() => setTab(item)}>{item}</button>
        ))}
      </nav>
      {error && <div className="error-banner">{error}</div>}
      <main>
        {summary.isError && <Empty title="Waiting for the API" detail={(summary.error as Error).message} />}
        {tab === 'overview' && <Overview summary={summary.data} />}
        {tab === 'workloads' && <Workloads />}
        {tab === 'recommendations' && <Recommendations onError={setError} />}
        {tab === 'policies' && <Policies onError={setError} />}
        {tab === 'executions' && <Executions onError={setError} />}
        {tab === 'audit' && <Audit />}
      </main>
    </div>
  )
}

function Policies({ onError }: { onError: (value: string) => void }) {
  const queryClient = useQueryClient()
  const provider = useQuery({ queryKey: ['llm'], queryFn: () => getJSON<LLMStatus>('/api/v1/llm') })
  const policies = useQuery({ queryKey: ['policies'], queryFn: () => getJSON<PolicyVersion[]>('/api/v1/policies') })
  const [requirements, setRequirements] = useState('Development can shut down after 7 PM, but keep payment simulations running and never modify workloads labeled customer-demo=true.')
  const [draft, setDraft] = useState<PolicyDraft>()
  const [streamedText, setStreamedText] = useState('')
  const create = useMutation({
    mutationFn: () => streamPolicyDraft<PolicyDraft>(requirements, (delta) => setStreamedText((current) => current + delta)),
    onMutate: () => { setDraft(undefined); setStreamedText(''); onError('') },
    onSuccess: (value) => { setDraft(value); void policies.refetch(); onError('') },
    onError: (error: Error) => onError(error.message),
  })
  const remove = useMutation({
    mutationFn: (id: string) => deleteJSON(`/api/v1/policies/${id}`),
    onSuccess: (_, id) => {
      if (draft?.id === id) {
        setDraft(undefined)
        setStreamedText('')
      }
      void policies.refetch()
      onError('')
    },
    onError: (error: Error) => onError(error.message),
  })
  const transition = useMutation({
    mutationFn: ({ id, verb }: { id: string; verb: 'activate' | 'deactivate' }) => postJSON(`/api/v1/policies/${id}/${verb}`),
    onSuccess: (_, variables) => {
      if (variables.verb === 'activate' && draft?.id === variables.id) setDraft(undefined)
      void queryClient.invalidateQueries()
      onError('')
    },
    onError: (error: Error) => onError(error.message),
  })
  if (!policies.data) return <Loading />
  const active = policies.data?.find((item) => item.status === 'active')
  return <section className="page"><PageTitle eyebrow="RECOMMENDATION GUARDRAILS" title="Policies" detail="The active policy decides which workloads analysis may recommend changing. AI can draft a policy, but a person must activate it."/>
    <div className={`policy-state ${active ? 'has-active' : ''}`}><div><p className="eyebrow">{active ? `ACTIVE POLICY · VERSION ${active.version}` : 'NO ACTIVE POLICY'}</p><h2>{active ? active.sourceText : 'Analysis is paused'}</h2><p>{active ? 'New analysis uses this version. Existing approvals remain bound to the version and plan hash they were created with.' : 'Activate a reviewed draft before KubeSqueeze can propose workload changes.'}</p></div>{active && <button className="secondary" disabled={transition.isPending} onClick={() => { if (window.confirm(`Deactivate policy version ${active.version}? Open recommendations under this policy will expire.`)) transition.mutate({ id: active.id, verb: 'deactivate' }) }}>Deactivate</button>}</div>
    <div className="policy-builder"><article className="panel"><div className="badges"><span className={provider.data?.enabled ? 'safe' : ''}>{provider.data?.enabled ? provider.data.model : 'provider disabled'}</span><span>drafting only</span></div><label htmlFor="requirements">Operating requirements</label><textarea id="requirements" rows={7} value={requirements} onChange={(event) => setRequirements(event.target.value)} /><button className="primary" disabled={!provider.data?.enabled || create.isPending} onClick={() => create.mutate()}>{create.isPending ? 'Generating & validating…' : 'Generate typed draft'}</button>{!provider.data?.enabled && <p className="hint">Policy drafting requires LLM_BASE_URL and LLM_MODEL. Existing drafts and policy controls remain available.</p>}</article>
    <article className="panel"><p className="eyebrow">DRAFT PREVIEW</p>{draft ? <><h2>Version {draft.version}</h2><pre>{JSON.stringify(draft.policy, null, 2)}</pre><p className="hint">Validated and saved as a draft. Review it below before activation.</p></> : streamedText ? <><h2>{create.isPending ? 'Generating…' : 'Draft rejected'}</h2><pre className="stream-output" aria-live="polite">{streamedText}{create.isPending && <span className="stream-cursor" />}</pre><p className="hint">{create.isPending ? 'This preview is not saved until validation succeeds.' : 'This output was not saved because generation or validation failed.'}</p></> : <Empty title="No draft generated" detail="Model output must pass the typed safety validator before it is stored."/>}</article></div>
    <h2 className="section-title">Saved policies <span>{policies.data?.length ?? 0}</span></h2>
    <div className="policy-list">{policies.data?.map((item) => <article className="panel" key={item.id}><div><div className="badges"><span className={item.status === 'active' ? 'safe' : ''}>{item.status === 'superseded' ? 'inactive' : item.status}</span><span>version {item.version}</span></div><p>{item.sourceText || 'No source requirements recorded.'}</p><small>{item.approvedBy ? `Activated by ${item.approvedBy} · ` : ''}Created {new Date(item.createdAt).toLocaleString()}</small></div><div className="policy-actions">{item.status === 'draft' && <button className="primary" disabled={transition.isPending} onClick={() => { if (window.confirm(`Activate policy version ${item.version}? It will replace the current active policy and refresh recommendations.`)) transition.mutate({ id: item.id, verb: 'activate' }) }}>Activate</button>}{item.status === 'active' && <button className="secondary" disabled={transition.isPending} onClick={() => { if (window.confirm(`Deactivate policy version ${item.version}? Open recommendations under this policy will expire.`)) transition.mutate({ id: item.id, verb: 'deactivate' }) }}>Deactivate</button>}{item.status !== 'active' && <button className="danger" disabled={remove.isPending && remove.variables === item.id} onClick={() => { if (window.confirm(`Permanently delete policy version ${item.version}?`)) remove.mutate(item.id) }}>{remove.isPending && remove.variables === item.id ? 'Deleting…' : 'Delete'}</button>}</div></article>)}</div>
  </section>
}

function Overview({ summary }: { summary?: Summary }) {
  const policies = useQuery({ queryKey: ['policies'], queryFn: () => getJSON<PolicyVersion[]>('/api/v1/policies') })
  if (!summary) return <Loading />
  const activePolicy = policies.data?.find((item) => item.status === 'active')
  const cards = [
    ['Potential savings', money(summary.potentialMonthlySavings) + '/mo', 'Capacity opportunity, not realized spend'],
    ['Workloads', String(summary.workloads), `${summary.proposedRecommendations} actionable recommendations`],
    ['Cluster capacity', `${(summary.allocatableCpuMilli / 1000).toFixed(0)} cores`, `${summary.nodeCount} Kind nodes`],
    ['Safety record', String(summary.rollbacks), 'automatic rollbacks demonstrated'],
  ]
  return <>
    <section className="hero"><div><p className="eyebrow">ACME PREVIEW LABS</p><h1>Spend less without<br/><em>guessing at reliability.</em></h1><p>Every recommendation is backed by cluster state, seven days of utilization evidence, an approved policy, and a restorable execution plan.</p></div><div className="hero-status"><span>Live environment</span><strong>{summary.kubernetesVersion || 'discovering'}</strong><small>Last analyzed {summary.lastCollectedAt ? ago(summary.lastCollectedAt) : 'never'}</small></div></section>
    <section className="cards">{cards.map(([label, value, detail]) => <article className="metric-card" key={label}><span>{label}</span><strong>{value}</strong><small>{detail}</small></article>)}</section>
    <section className="story-grid"><article className="panel"><p className="eyebrow">CUSTOMER STORY</p><h2>The bill increased 40%.</h2><p>Preview environments multiplied, development stayed awake overnight, and oversized replicas prevented node consolidation.</p><div className="delta"><span>Previous baseline</span><b>$465/mo</b><span>Current projection</span><b>$651/mo</b></div></article><article className="panel policy"><p className="eyebrow">{activePolicy ? `ACTIVE POLICY · V${activePolicy.version}` : 'NO ACTIVE POLICY'}</p><blockquote>{activePolicy ? `“${activePolicy.sourceText}”` : 'Recommendations are paused until a reviewed policy is activated.'}</blockquote><ul><li>Production changes denied</li><li>Human approval required</li><li>Failed health checks restore prior state</li></ul></article></section>
  </>
}

function Workloads() {
  const query = useQuery({ queryKey: ['workloads'], queryFn: () => getJSON<Workload[]>('/api/v1/workloads') })
  const [selected, setSelected] = useState<Workload>()
  const columns = useMemo<ColumnDef<Workload>[]>(() => [
    { header: 'Workload', cell: ({ row }) => <div><b>{row.original.name}</b><small>{row.original.namespace} · {row.original.kind}</small></div> },
    { header: 'Environment', accessorKey: 'environment' },
    { header: 'Ready', cell: ({ row }) => `${row.original.readyReplicas}/${row.original.replicas}` },
    { header: 'CPU request', cell: ({ row }) => `${row.original.cpuRequestMilli}m` },
    { header: '7d p95', cell: ({ row }) => row.original.metricP95CpuCores == null ? 'No data' : `${row.original.metricP95CpuCores.toFixed(3)} cores` },
    { header: 'Guards', cell: ({ row }) => <div className="badges">{row.original.hasHpa && <span>HPA</span>}{row.original.pdbDesiredHealthy != null && <span>PDB min {row.original.pdbDesiredHealthy}</span>}</div> },
  ], [])
  if (!query.data) return <Loading />
  return <section className="page"><PageTitle eyebrow="DISCOVER" title="Cluster workloads" detail="Normalized Kubernetes state joined with historical Prometheus evidence."/><DataTable data={query.data} columns={columns} onRow={setSelected}/>{selected && <History workload={selected} onClose={() => setSelected(undefined)} />}</section>
}

function Recommendations({ onError }: { onError: (value: string) => void }) {
  const queryClient = useQueryClient()
  const query = useQuery({ queryKey: ['recommendations'], queryFn: () => getJSON<Recommendation[]>('/api/v1/recommendations') })
  const policies = useQuery({ queryKey: ['policies'], queryFn: () => getJSON<PolicyVersion[]>('/api/v1/policies') })
  const action = useMutation({ mutationFn: ({ id, verb }: { id: string; verb: string }) => postJSON(`/api/v1/recommendations/${id}/${verb}`), onSuccess: () => { void queryClient.invalidateQueries(); onError('') }, onError: (e: Error) => onError(e.message) })
  if (!query.data || !policies.data) return <Loading />
  const activePolicy = policies.data?.find((item) => item.status === 'active')
  const actionable = query.data.filter((item) => ['proposed', 'approved', 'queued', 'executing'].includes(item.status))
  const rejected = query.data.filter((item) => item.status === 'rejected')
  const previous = query.data.filter((item) => !['proposed', 'approved', 'queued', 'executing', 'rejected'].includes(item.status))
  return <section className="page"><PageTitle eyebrow="RECOMMEND" title="Evidence-backed plans" detail="Approval binds the exact action, evidence snapshot, policy version, and plan hash."/>
    <div className={`recommendation-context ${activePolicy ? 'ready' : ''}`}><div><span className="context-dot"/><div><strong>{activePolicy ? `Policy version ${activePolicy.version} is active` : 'No active policy'}</strong><p>{activePolicy ? `${actionable.length} open plan${actionable.length === 1 ? '' : 's'} and ${rejected.length} safety rejection${rejected.length === 1 ? '' : 's'} from the latest analysis.` : 'Activate a policy before running analysis. No workload changes can be proposed without one.'}</p></div></div></div>
    {actionable.length === 0 && <Empty title={activePolicy ? 'No actionable recommendations' : 'Recommendations are paused'} detail={activePolicy ? (rejected.length ? 'Every analyzed workload was blocked by a policy or safety rule. Review the reasons below.' : 'Run analysis to collect workload evidence and generate plans.') : 'Go to Policies and activate a reviewed draft first.'}/>}<div className="recommendation-list">{actionable.map((item) => <article className="recommendation" key={item.id}><div className="rec-main"><div className="badges"><span className="safe">{item.actionType}</span><span>{item.status}</span><span>policy v{item.policyVersion}</span></div><h3>{item.namespace} / {item.workloadName}</h3><p>{item.explanation}</p><details><summary>Inspect evidence and plan hash</summary><pre>{JSON.stringify(item.evidence, null, 2)}</pre><code>{item.planHash}</code></details></div><div className="rec-impact"><span>Replicas</span><strong>{item.currentReplicas} → {item.targetReplicas}</strong><span>Potential</span><strong>{money(item.potentialMonthlySavings)}/mo</strong>{item.status === 'proposed' && <button className="primary" disabled={action.isPending} onClick={() => action.mutate({ id: item.id, verb: 'approve' })}>Approve plan</button>}{item.status === 'approved' && <button className="danger" disabled={action.isPending} onClick={() => action.mutate({ id: item.id, verb: 'execute' })}>Run squeeze</button>}</div></article>)}</div>
    <h2 className="section-title">Rejected by safety policy <span>{rejected.length}</span></h2><div className="rejected-grid">{rejected.map((item) => <article className="rejected" key={item.id}><span>{item.reasonCode}</span><h3>{item.namespace} / {item.workloadName}</h3><p>{item.explanation}</p></article>)}</div>
    {previous.length > 0 && <details className="previous-plans"><summary>Previous and expired plans ({previous.length})</summary><div>{previous.map((item) => <p key={item.id}><span>{item.status}</span>{item.namespace} / {item.workloadName} · policy v{item.policyVersion}</p>)}</div></details>}
  </section>
}

function Executions({ onError }: { onError: (value: string) => void }) {
  const query = useQuery({ queryKey: ['executions'], queryFn: () => getJSON<Execution[]>('/api/v1/executions') })
  const restore = useMutation({ mutationFn: (id: string) => postJSON(`/api/v1/executions/${id}/restore`), onError: (e: Error) => onError(e.message) })
  if (!query.data) return <Loading />
  return <section className="page"><PageTitle eyebrow="VERIFY & RESTORE" title="Execution history" detail="Squeeze, verification, rollback, and human restore are recorded as durable state transitions."/><div className="timeline">{query.data.map((item) => <article key={item.id}><div className={`status-icon ${item.status}`}>{item.status === 'succeeded' ? '✓' : item.status === 'rolled_back' ? '↶' : '•'}</div><div><div className="badges"><span>{item.status}</span></div><h3>{item.namespace} / {item.workloadName}</h3><p>{item.previousReplicas} → {item.targetReplicas} replicas · {new Date(item.startedAt).toLocaleString()}</p>{item.rollbackReason && <blockquote>{item.rollbackReason}</blockquote>}</div>{item.status === 'succeeded' && <button onClick={() => restore.mutate(item.id)}>Restore</button>}</article>)}</div>{query.data.length === 0 && <Empty title="No executions yet" detail="Approve a safe recommendation, then run its squeeze plan."/>}</section>
}

function Audit() {
  const query = useQuery({ queryKey: ['audit'], queryFn: () => getJSON<AuditEvent[]>('/api/v1/audit') })
  if (!query.data) return <Loading />
  return <section className="page"><PageTitle eyebrow="REPORT" title="Immutable audit trail" detail="Human decisions and agent actions are attributable and timestamped."/><div className="audit-list">{query.data.map((item) => <article key={item.id}><time>{new Date(item.createdAt).toLocaleTimeString()}</time><div><strong>{item.eventType}</strong><p>{item.actorType} · {item.actorId} · {item.objectType}/{item.objectId}</p></div><pre>{JSON.stringify(item.detail)}</pre></article>)}</div></section>
}

function History({ workload, onClose }: { workload: Workload; onClose: () => void }) {
  const query = useQuery({ queryKey: ['history', workload.namespace, workload.name], queryFn: () => getJSON<ChartPoint[]>(`/api/v1/history?namespace=${encodeURIComponent(workload.namespace)}&workload=${encodeURIComponent(workload.name)}`) })
  const points = query.data ?? []
  const max = Math.max(...points.map((item) => item.value), 0.1)
  const path = points.map((item, index) => `${index ? 'L' : 'M'} ${(index / Math.max(points.length - 1, 1)) * 700} ${190 - (item.value / max) * 160}`).join(' ')
  return <div className="drawer"><div className="drawer-card"><button className="close" onClick={onClose}>×</button><p className="eyebrow">PROMETHEUS · 7 DAYS</p><h2>{workload.namespace} / {workload.name}</h2>{query.isLoading ? <Loading/> : <svg viewBox="0 0 700 210" role="img"><line x1="0" y1="190" x2="700" y2="190"/><path d={path}/></svg>}<div className="chart-legend"><span>p95 <b>{workload.metricP95CpuCores?.toFixed(3) ?? '—'} cores</b></span><span>Samples <b>{points.length}</b></span><span>Coverage <b>{(workload.metricCoverage * 100).toFixed(0)}%</b></span></div></div></div>
}

function DataTable<T>({ data, columns, onRow }: { data: T[]; columns: ColumnDef<T>[]; onRow?: (value: T) => void }) {
  const table = useReactTable({ data, columns, getCoreRowModel: getCoreRowModel() })
  return <div className="table-wrap"><table><thead>{table.getHeaderGroups().map((group) => <tr key={group.id}>{group.headers.map((header) => <th key={header.id}>{flexRender(header.column.columnDef.header, header.getContext())}</th>)}</tr>)}</thead><tbody>{table.getRowModel().rows.map((row) => <tr key={row.id} onClick={() => onRow?.(row.original)}>{row.getVisibleCells().map((cell) => <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>)}</tr>)}</tbody></table></div>
}

function PageTitle({ eyebrow, title, detail }: { eyebrow: string; title: string; detail: string }) { return <header className="page-title"><p className="eyebrow">{eyebrow}</p><h1>{title}</h1><p>{detail}</p></header> }
function Loading() { return <div className="loading"><span/>Collecting evidence…</div> }
function Empty({ title, detail }: { title: string; detail: string }) { return <div className="empty"><h2>{title}</h2><p>{detail}</p></div> }
function money(value: number) { return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 0 }).format(value || 0) }
function ago(value: string) { const seconds = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 1000)); return seconds < 60 ? `${seconds}s ago` : `${Math.floor(seconds / 60)}m ago` }
