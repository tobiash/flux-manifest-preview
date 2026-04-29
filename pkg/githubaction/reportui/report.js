(() => {
  const data = JSON.parse(document.getElementById('fmp-report-data').textContent)
  const app = document.getElementById('app')
  const filters = {query: '', action: '', kind: '', namespace: '', producer: ''}
  let focusedIndex = -1

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
    const [name, query] = hash.split('?')
    return {name, params: new URLSearchParams(query || '')}
  }

  function setActiveNav(name) {
    document.querySelectorAll('[data-route]').forEach(a => a.classList.toggle('active', a.dataset.route === name))
  }

  function render() {
    const r = route()
    setActiveNav(r.name === 'resource' ? 'resources' : r.name)
    focusedIndex = -1
    app.replaceChildren()
    if (r.name === 'resources') renderResources(r.params)
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
      metric('added', `+${data.summary.added}`, 'New resources', 'added'),
      metric('modified', `~${data.summary.modified}`, 'Modified resources', 'modified'),
      metric('deleted', `-${data.summary.deleted}`, 'Deleted resources', 'deleted'),
      metric('', `${data.summary.total}`, 'Total changes', '')
    ])
  }

  function metric(kind, value, label, action) {
    const href = action !== undefined ? `#resources${action ? '?action=' + action : ''}` : null
    if (href) return el('a', {href, class: `metric ${kind}`}, [el('span', {class: 'metric-value', text: value}), el('span', {class: 'metric-label', text: label})])
    return el('div', {class: `metric ${kind}`}, [el('span', {class: 'metric-value', text: value}), el('span', {class: 'metric-label', text: label})])
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

  function renderResources(params) {
    if (params) {
      const action = params.get('action')
      if (action) filters.action = action
    }
    app.replaceChildren()
    app.append(el('div', {class: 'eyebrow', text: 'Resource Browser'}), el('h1', {text: 'Changed Resources'}), filterBar())
    renderFilteredList()
  }

  function renderFilteredList() {
    let list = app.querySelector('.resource-list')
    if (list) list.replaceChildren()
    else { list = el('div', {class: 'resource-list'}); app.append(list) }
    const rows = filteredResources()
    for (const res of rows) list.append(resourceCard(res))
    if (rows.length === 0) list.append(el('div', {class: 'empty-state', text: 'No resources match the current filters.'}))
    const count = app.querySelector('.filter-count')
    if (count) count.textContent = `${rows.length} of ${data.resources.length} resources`
    focusedIndex = -1
  }

  function filterBar() {
    const bar = el('div', {class: 'filterbar'})
    const search = el('input', {placeholder: 'Search name, namespace, kind, producer', value: filters.query})
    search.addEventListener('input', () => { filters.query = search.value; renderFilteredList() })
    bar.append(search, select('action', [''].concat(unique(data.resources.map(r => r.action)))), select('kind', [''].concat(unique(data.resources.map(r => r.kind)))), select('namespace', [''].concat(unique(data.resources.map(r => r.namespace || 'cluster-scoped')))), select('producer', [''].concat(unique(data.resources.map(r => r.producer || 'unknown')))), el('span', {class: 'filter-count', text: `${filteredResources().length} of ${data.resources.length} resources`}))
    return bar
  }

  function select(key, values) {
    const node = el('select')
    for (const value of values) node.append(el('option', {value, text: value || `All ${key}s`}))
    node.value = filters[key]
    node.addEventListener('change', () => { filters[key] = node.value; renderFilteredList() })
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

  function sparkline(res) {
    const total = res.addedLines + res.deletedLines
    if (total === 0) return el('div', {class: 'sparkline'})
    const wrap = el('div', {class: 'sparkline'})
    const addedPct = (res.addedLines / total * 100).toFixed(1)
    const deletedPct = (res.deletedLines / total * 100).toFixed(1)
    const addedBar = el('div', {class: 'sparkline-bar added', style: `width:${addedPct}%`})
    const deletedBar = el('div', {class: 'sparkline-bar deleted', style: `width:${deletedPct}%;left:${addedPct}%`})
    wrap.append(addedBar, deletedBar)
    return wrap
  }

  function resourceCard(res) {
    return el('a', {class: 'resource-card', href: `#resource/${res.index}`, tabindex: '0'}, [
      el('div', {class: 'resource-title'}, [
        el('strong', {text: `${res.kind} / ${res.namespace || 'cluster-scoped'} / ${res.name}`}),
        el('span', {class: `pill ${res.action}`, text: res.action})
      ]),
      el('div', {class: 'resource-meta', text: `${res.apiVersion} · Producer: ${res.producer || 'unknown'}`}),
      el('div', {class: 'resource-foot'}, [
        el('span', {text: `+${res.addedLines}`}),
        el('span', {text: `-${res.deletedLines}`}),
        sparkline(res)
      ])
    ])
  }

  function diffStats(res) {
    let added = 0, deleted = 0, context = 0
    for (const row of res.diffRows) {
      if (row.type === 'added') added++
      else if (row.type === 'deleted') deleted++
      else if (row.type === 'context') context++
    }
    return el('div', {class: 'diff-stats'}, [
      el('span', {class: 'added', text: `+${added} added`}),
      el('span', {class: 'deleted', text: `-${deleted} deleted`}),
      el('span', {class: 'context', text: `${context} unchanged`})
    ])
  }

  function copyButton(res) {
    const btn = el('button', {class: 'copy-btn', text: 'Copy diff'})
    btn.addEventListener('click', () => {
      const lines = res.diffRows.map(row => {
        if (row.type === 'hunk') return row.oldText
        const sign = row.type === 'added' ? '+' : row.type === 'deleted' ? '-' : ' '
        return sign + (row.type === 'added' ? row.newText : row.oldText)
      })
      navigator.clipboard.writeText(lines.join('\n')).then(() => {
        btn.textContent = 'Copied'
        btn.classList.add('copied')
        setTimeout(() => { btn.textContent = 'Copy diff'; btn.classList.remove('copied') }, 1500)
      })
    })
    return btn
  }

  function renderResource(index, view) {
    const res = data.resources[index]
    if (!res) { app.append(el('div', {class: 'empty-state', text: 'Resource not found.'})); return }
    app.append(
      el('div', {class: 'breadcrumb'}, [el('a', {href: '#resources', text: 'Resources'}), document.createTextNode(` / ${res.kind} / ${res.name}`)]),
      el('section', {class: 'detail-header'}, [
        el('div', {}, [el('div', {class: 'eyebrow', text: res.action}), el('h1', {text: `${res.kind} ${res.name}`}), el('p', {text: `${res.namespace || 'cluster-scoped'} · ${res.apiVersion} · Producer: ${res.producer || 'unknown'}`})]),
        el('div', {class: 'detail-actions'}, [diffToggle(index, view), copyButton(res)])
      ]),
      diffStats(res),
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

  function handleKeydown(e) {
    const cards = () => [...app.querySelectorAll('.resource-card')]
    const r = route()

    if (r.name === 'resources') {
      const all = cards()
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        focusedIndex = Math.min(focusedIndex + 1, all.length - 1)
        all[focusedIndex]?.focus()
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        focusedIndex = Math.max(focusedIndex - 1, 0)
        all[focusedIndex]?.focus()
      } else if (e.key === 'Enter' && focusedIndex >= 0 && all[focusedIndex]) {
        location.hash = all[focusedIndex].getAttribute('href').replace(/^.*#/, '#')
      }
    }

    if (r.name === 'resource' && e.key === 'Escape') {
      e.preventDefault()
      location.hash = '#resources'
    }
  }

  window.addEventListener('hashchange', render)
  window.addEventListener('keydown', handleKeydown)
  render()
})()
