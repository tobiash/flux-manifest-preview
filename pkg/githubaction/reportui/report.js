(() => {
  const data = JSON.parse(document.getElementById('fmp-report-data').textContent)
  const app = document.getElementById('app')
  const filters = {query: '', action: '', kind: '', namespace: '', producer: ''}

  function el(tag, attrs = {}, children = []) {
    const node = document.createElement(tag)
    for (const [key, value] of Object.entries(attrs)) {
      if (key === 'class') node.className = value
      else if (key === 'text') node.textContent = value
      else node.setAttribute(key, value)
    }
    for (const child of children) node.append(child)
    return node
  }

  function route() {
    const hash = location.hash.replace(/^#/, '') || 'overview'
    if (hash.startsWith('resource/')) {
      const [path, query] = hash.split('?')
      return {name: 'resource', index: Number(path.split('/')[1]), view: new URLSearchParams(query || '').get('view') || 'unified'}
    }
    return {name: hash}
  }

  function setActiveNav(name) {
    document.querySelectorAll('[data-route]').forEach(a => a.classList.toggle('active', a.dataset.route === name))
  }

  function render() {
    const r = route()
    setActiveNav(r.name === 'resource' ? 'resources' : r.name)
    app.replaceChildren()
    if (r.name === 'resources') renderResources()
    else if (r.name === 'resource') renderResource(r.index, r.view)
    else renderOverview()
    app.focus({preventScroll: true})
  }

  function renderOverview() {
    app.append(
      el('div', {class: 'eyebrow', text: 'Impact Summary'}),
      el('h1', {text: titleForStatus()}),
      el('div', {class: 'meta'}, [
        el('span', {text: `Base: ${data.meta.base || 'unknown'}`}),
        el('span', {text: `Target: ${data.meta.target || 'unknown'}`}),
        el('span', {text: `Generated: ${data.meta.generatedAt}`})
      ]),
      summaryGrid(),
      actionLinks()
    )
    renderKindSection()
    renderPolicySection()
    renderTopChanges()
  }

  function titleForStatus() {
    if (data.summary.total === 0) return 'No Manifest Changes'
    if (data.policies.policyFailed || data.meta.status === 'error') return 'Review Required'
    return 'Manifest Changes Detected'
  }

  function summaryGrid() {
    return el('section', {class: 'summary-grid'}, [
      metric('added', `+${data.summary.added}`, 'New resources'),
      metric('modified', `~${data.summary.modified}`, 'Modified resources'),
      metric('deleted', `-${data.summary.deleted}`, 'Deleted resources'),
      metric('', `${data.summary.total}`, 'Total changes')
    ])
  }

  function metric(kind, value, label) {
    return el('div', {class: `metric ${kind}`}, [
      el('span', {class: 'metric-value', text: value}),
      el('span', {class: 'metric-label', text: label})
    ])
  }

  function actionLinks() {
    return el('div', {class: 'overview-actions'}, [
      el('a', {class: 'button', href: '#resources', text: 'Open Resource Browser'}),
      el('a', {class: 'button secondary', href: '#overview', text: `${data.resources.length} resources indexed`})
    ])
  }

  function renderKindSection() {
    const rows = Object.entries(data.summary.kindBreakdown || {}).sort((a, b) => b[1].total - a[1].total || a[0].localeCompare(b[0]))
    if (rows.length === 0) return
    app.append(el('h2', {text: 'Classifications'}))
    const grid = el('div', {class: 'kind-grid'})
    for (const [kind, row] of rows) {
      grid.append(el('div', {class: 'card'}, [
        el('div', {class: 'card-title'}, [el('strong', {text: kind}), el('span', {class: 'pill', text: String(row.total)})]),
        el('small', {text: `${row.added} added · ${row.modified} modified · ${row.deleted} deleted`})
      ]))
    }
    app.append(grid)
  }

  function renderPolicySection() {
    const items = []
    for (const item of data.policies.classifications || []) items.push(`Classification: ${item.id || item.ID || 'unknown'}`)
    for (const item of data.policies.violations || []) items.push(`Violation: ${item.id || item.ID || item.message || 'unknown'}`)
    for (const label of data.policies.labels || []) items.push(`Label: ${label}`)
    if (items.length === 0) return
    app.append(el('h2', {text: 'Policy Signals'}))
    const cards = el('div', {class: 'cards'})
    for (const item of items) cards.append(el('div', {class: 'card'}, [el('span', {class: 'pill error', text: item})]))
    app.append(cards)
  }

  function renderTopChanges() {
    app.append(el('h2', {text: 'Changes'}))
    const list = el('div', {class: 'resource-list'})
    for (const res of data.resources.slice(0, 8)) list.append(resourceCard(res))
    if (data.resources.length === 0) list.append(el('div', {class: 'empty-state', text: 'No changed resources.'}))
    app.append(list)
  }

  function renderResources() {
    app.append(el('div', {class: 'eyebrow', text: 'Resource Browser'}), el('h1', {text: 'Changed Resources'}), filterBar())
    const list = el('div', {class: 'resource-list'})
    const rows = filteredResources()
    for (const res of rows) list.append(resourceCard(res))
    if (rows.length === 0) list.append(el('div', {class: 'empty-state', text: 'No resources match the current filters.'}))
    app.append(list)
  }

  function filterBar() {
    const bar = el('div', {class: 'filterbar'})
    const search = el('input', {placeholder: 'Search name, namespace, kind, producer', value: filters.query})
    search.addEventListener('input', () => { filters.query = search.value.toLowerCase(); renderResources() })
    bar.append(search, select('action', [''].concat(unique(data.resources.map(r => r.action)))), select('kind', [''].concat(unique(data.resources.map(r => r.kind)))), select('namespace', [''].concat(unique(data.resources.map(r => r.namespace || 'cluster-scoped')))), select('producer', [''].concat(unique(data.resources.map(r => r.producer || 'unknown')))))
    return bar
  }

  function select(key, values) {
    const node = el('select')
    for (const value of values) node.append(el('option', {value, text: value || `All ${key}s`}))
    node.value = filters[key]
    node.addEventListener('change', () => { filters[key] = node.value; renderResources() })
    return node
  }

  function unique(values) {
    return [...new Set(values)].filter(Boolean).sort((a, b) => a.localeCompare(b))
  }

  function filteredResources() {
    return data.resources.filter(r => {
      const haystack = `${r.name} ${r.namespace || 'cluster-scoped'} ${r.kind} ${r.producer || ''}`.toLowerCase()
      return (!filters.query || haystack.includes(filters.query)) &&
        (!filters.action || r.action === filters.action) &&
        (!filters.kind || r.kind === filters.kind) &&
        (!filters.namespace || (r.namespace || 'cluster-scoped') === filters.namespace) &&
        (!filters.producer || (r.producer || 'unknown') === filters.producer)
    })
  }

  function resourceCard(res) {
    return el('a', {class: 'resource-card', href: `#resource/${res.index}`}, [
      el('div', {class: 'resource-title'}, [
        el('strong', {text: `${res.kind} / ${res.namespace || 'cluster-scoped'} / ${res.name}`}),
        el('span', {class: `pill ${res.action}`, text: res.action})
      ]),
      el('div', {class: 'resource-meta', text: `${res.apiVersion} · Producer: ${res.producer || 'unknown'}`}),
      el('div', {class: 'resource-foot'}, [el('span', {text: `+${res.addedLines}`}), el('span', {text: `-${res.deletedLines}`})])
    ])
  }

  function renderResource(index, view) {
    const res = data.resources[index]
    if (!res) { app.append(el('div', {class: 'empty-state', text: 'Resource not found.'})); return }
    app.append(
      el('div', {class: 'breadcrumb'}, [el('a', {href: '#resources', text: 'Resources'}), document.createTextNode(` / ${res.kind} / ${res.name}`)]),
      el('section', {class: 'detail-header'}, [
        el('div', {}, [el('div', {class: 'eyebrow', text: res.action}), el('h1', {text: `${res.kind} ${res.name}`}), el('p', {text: `${res.namespace || 'cluster-scoped'} · ${res.apiVersion} · Producer: ${res.producer || 'unknown'}`})]),
        diffToggle(index, view)
      ]),
      el('section', {class: 'detail-panel'}, [diffView(res, view)])
    )
  }

  function diffToggle(index, view) {
    const toggle = el('div', {class: 'toggle'})
    for (const mode of ['unified', 'split']) {
      const button = el('button', {class: view === mode ? 'active' : '', text: mode === 'split' ? 'Split' : 'Unified'})
      button.addEventListener('click', () => { location.hash = `resource/${index}?view=${mode}` })
      toggle.append(button)
    }
    return toggle
  }

  function diffView(res, view) {
    const wrap = el('div', {class: `diff ${view}`})
    for (const row of res.diffRows) wrap.append(view === 'split' ? splitRow(row) : unifiedRow(row))
    if (res.truncated) wrap.append(el('div', {class: 'empty-state', text: 'Diff truncated by html-report-max-resource-diff-bytes.'}))
    return wrap
  }

  function unifiedRow(row) {
    const sign = row.type === 'added' ? '+' : row.type === 'deleted' ? '-' : ' '
    const number = row.type === 'added' ? row.newLine : row.oldLine || row.newLine
    const text = row.type === 'hunk' ? row.oldText : `${sign}${row.type === 'added' ? row.newText : row.oldText}`
    return el('div', {class: `diff-row ${row.type}`}, [el('div', {class: 'cell line-no', text: row.type === 'hunk' ? '' : String(number || '')}), el('div', {class: 'cell', text})])
  }

  function splitRow(row) {
    if (row.type === 'hunk') return el('div', {class: 'diff-row hunk'}, [el('div', {class: 'cell line-no'}), el('div', {class: 'cell', text: row.oldText}), el('div', {class: 'cell line-no'}), el('div', {class: 'cell', text: row.newText})])
    return el('div', {class: `diff-row ${row.type}`}, [
      el('div', {class: 'cell line-no', text: row.oldLine ? String(row.oldLine) : ''}),
      el('div', {class: `cell ${row.oldLine ? '' : 'empty'}`, text: row.oldText || ''}),
      el('div', {class: 'cell line-no', text: row.newLine ? String(row.newLine) : ''}),
      el('div', {class: `cell ${row.newLine ? '' : 'empty'}`, text: row.newText || ''})
    ])
  }

  window.addEventListener('hashchange', render)
  render()
})()
