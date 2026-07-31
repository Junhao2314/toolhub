import { expect, test, type Page } from '@playwright/test'

const now = '2026-07-30T04:00:00Z'

async function expectMobileNavigationClosed(page: Page, projectName: string) {
  if (projectName !== 'mobile') return
  await expect(page.locator('.nav-scrim')).toHaveCount(0)
  await expect.poll(async () => {
    const box = await page.locator('.sidebar').boundingBox()
    return box ? Math.round(box.x + box.width) : 0
  }).toBeLessThanOrEqual(0)
}

async function navigate(page: Page, projectName: string, label: string) {
  if (projectName === 'mobile') await page.getByRole('button', { name: 'Open navigation' }).click()
  await page.getByRole('button', { name: label, exact: true }).click()
  await expectMobileNavigationClosed(page, projectName)
  await expect(page.getByRole('heading', { name: label, exact: true })).toBeVisible()
}

async function expectNoViewportOverflow(page: Page) {
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true)
}

function session() {
  return { authenticated: true, csrfToken: 'fixture-csrf', expiresAt: '2099-01-01T00:00:00Z', user: { username: 'admin', passwordChangeRecommended: false } }
}

test('username login and generation-2 navigation render without overlap', async ({ page }, testInfo) => {
  const username = process.env.TOOLHUB_E2E_USERNAME
  const password = process.env.TOOLHUB_E2E_PASSWORD
  if (!username || !password) throw new Error('TOOLHUB_E2E_USERNAME and TOOLHUB_E2E_PASSWORD are required')
  const consoleErrors: string[] = []
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()) })

  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Overview', exact: true })).toBeVisible()
  if (process.env.TOOLHUB_E2E_EXPECT_PASSWORD_CHANGE === 'true') {
    await expect(page.getByText('This account is using a temporary password. Change it from Account.')).toBeVisible()
  }

  for (const label of ['Skills', 'Marketplace', 'MCP', 'Profiles', 'Targets', 'Operations', 'Settings', 'Account', 'Overview']) {
    await navigate(page, testInfo.project.name, label)
    await expectNoViewportOverflow(page)
  }
  await navigate(page, testInfo.project.name, 'Account')
  await expect(page.getByLabel('Username')).toHaveValue(username)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-generation2-navigation.png`, fullPage: true })
  expect(consoleErrors).toEqual([])
})

test('Profile preflight pins confirmation tokens and partial operations remain actionable', async ({ page }, testInfo) => {
  const profileID = '22222222-2222-4222-8222-222222222222'
  const targetID = '11111111-1111-4111-8111-111111111111'
  const operationID = '33333333-3333-4333-8333-333333333333'
  const target = { id: targetID, targetKey: 'build-01/claude', nodeId: '44444444-4444-4444-8444-444444444444', nodeName: 'build-01', nodeKind: 'salt', saltMinionId: 'build-01', runtime: 'claude', managedUsername: 'runner', writable: true, health: 'healthy', desiredRevision: 4, targetRevision: 'target-revision' }
  let applyBody: { confirmationTokens?: string[] } | null = null
  let applyIdempotency = ''
  let retryIdempotency = ''

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/profiles' && request.method() === 'GET') return route.fulfill({ json: { items: [{ id: profileID, name: 'Research', description: 'Pinned fleet profile', revision: 7, skillIds: ['skill-id'], mcpServerIds: ['server-id'], createdAt: now, updatedAt: now }] } })
    if (path === '/api/v1/skills') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/targets') return route.fulfill({ json: { items: [target] } })
    if (path === `/api/v1/profiles/${profileID}/preflight`) {
      expect(request.headers()['idempotency-key']).toBeTruthy()
      expect(request.postDataJSON()).toEqual({ targetIds: [targetID] })
      return route.fulfill({ json: { items: [{ targetId: targetID, confirmationToken: 'one-use-confirmation', expiresAt: '2099-01-01T00:05:00Z', result: { targetRevision: 'target-revision', manifestHash: 'a'.repeat(64), diff: { add: [{ kind: 'skill', name: 'formatter' }], replace: [{ kind: 'mcp', name: 'search' }], delete: [{ kind: 'skill', name: 'old-skill' }], excluded: [{ kind: 'skill', name: '.system', reason: 'protected' }] } } }] } })
    }
    if (path === `/api/v1/profiles/${profileID}/apply`) {
      applyBody = request.postDataJSON()
      applyIdempotency = request.headers()['idempotency-key'] ?? ''
      return route.fulfill({ status: 202, json: { id: operationID, kind: 'apply', status: 'queued', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    }
    if (path === '/api/v1/operations' && request.method() === 'GET') {
      return route.fulfill({ json: { items: [{ id: operationID, kind: 'apply', status: 'partial', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now, targets: [{ id: '55555555-5555-4555-8555-555555555555', targetId: targetID, targetKey: 'build-01/claude', status: 'succeeded', attempt: 1, pendingRerun: false }, { id: '66666666-6666-4666-8666-666666666666', targetId: '77777777-7777-4777-8777-777777777777', targetKey: 'build-02/codex', status: 'failed', attempt: 1, pendingRerun: false, errorCode: 'target_unavailable', errorReason: 'Salt minion is offline' }] }] } })
    }
    if (path === `/api/v1/operations/${operationID}` && request.method() === 'GET') {
      return route.fulfill({ json: { id: operationID, kind: 'apply', status: 'partial', metadata: {}, cancelRequested: false, createdAt: now, startedAt: now, finishedAt: now, updatedAt: now, targets: [{ id: '55555555-5555-4555-8555-555555555555', targetId: targetID, targetKey: 'build-01/claude', status: 'succeeded', attempt: 1, pendingRerun: false, bridgeOperationId: '55555555-5555-4555-8555-555555555555', saltJid: '20260730040000123456', result: { health: 'healthy', repaired: false }, createdAt: now, startedAt: now, finishedAt: now, updatedAt: now }, { id: '66666666-6666-4666-8666-666666666666', targetId: '77777777-7777-4777-8777-777777777777', targetKey: 'build-02/codex', status: 'failed', attempt: 1, pendingRerun: false, errorCode: 'target_unavailable', errorReason: 'Salt minion is offline', createdAt: now, startedAt: now, finishedAt: now, updatedAt: now }] } })
    }
    if (path === `/api/v1/operations/${operationID}/retry-failed`) {
      retryIdempotency = request.headers()['idempotency-key'] ?? ''
      return route.fulfill({ status: 202, json: { id: '88888888-8888-4888-8888-888888888888', kind: 'apply', status: 'queued', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    }
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/profiles')
  await page.getByRole('button', { name: 'Preflight and Apply' }).click()
  const dialog = page.getByRole('dialog', { name: 'Apply Profile · Research' })
  await dialog.getByRole('checkbox').check()
  await dialog.getByRole('button', { name: 'Run preflight' }).click()
  await expect(dialog).toContainText('skill / formatter')
  await expect(dialog).toContainText('mcp / search')
  await expect(dialog).toContainText('skill / old-skill')
  await expect(dialog).toContainText('skill / .system (protected)')
  await dialog.getByRole('button', { name: 'Confirm Apply' }).click()
  await expect(page.getByText(/Apply queued/)).toBeVisible()
  expect(applyBody).toEqual({ confirmationTokens: ['one-use-confirmation'] })
  expect(applyIdempotency).toBeTruthy()

  await page.goto('/operations')
  await expect(page.getByRole('heading', { name: 'Operations' })).toBeVisible()
  await expect(page.getByText('partial', { exact: true })).toBeVisible()
  await expect(page.getByText('Salt minion is offline')).toBeVisible()
  await page.getByRole('button', { name: 'View details' }).click()
  const operationDialog = page.getByRole('dialog', { name: /Operation details/ })
  await expect(operationDialog).toContainText('55555555-5555-4555-8555-555555555555')
  await expect(operationDialog).toContainText('20260730040000123456')
  await expect(operationDialog).toContainText('"health": "healthy"')
  await operationDialog.getByRole('button', { name: 'Close' }).click()
  await page.getByRole('button', { name: 'Retry failed targets' }).click()
  await expect(page.getByText('Retry queued')).toBeVisible()
  expect(retryIdempotency).toBeTruthy()
  await expectNoViewportOverflow(page)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-partial-operation.png`, fullPage: true })
})

test('shared relay inventory, controls, and write-only MCP secrets use generation-2 contracts', async ({ page }, testInfo) => {
  const targetID = '11111111-1111-4111-8111-111111111111'
  const nodeID = '22222222-2222-4222-8222-222222222222'
  const serverID = '33333333-3333-4333-8333-333333333333'
  const target = { id: targetID, targetKey: 'local/shared-relay', nodeId: nodeID, nodeName: 'local', nodeKind: 'local', runtime: 'shared-relay', managedUsername: 'runner', writable: true, health: 'healthy', desiredRevision: 3, targetRevision: 'relay-revision', lastScannedAt: now }
  const server = { id: serverID, name: 'search', description: 'Shared search server', revision: 2, transport: 'http', args: [], url: 'https://example.invalid/mcp', envKeys: ['API_TOKEN'], headerKeys: [], contentHash: 'b'.repeat(64), createdAt: now, updatedAt: now }
  let relayAction = ''
  let secretUpdate: { env?: Record<string, string> } | null = null
  let secretIdempotency = ''

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/targets') return route.fulfill({ json: { items: [target] } })
    if (path === '/api/v1/skills') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/mcp/servers' && request.method() === 'GET') return route.fulfill({ json: { items: [server] } })
    if (path === `/api/v1/targets/${targetID}`) {
      return route.fulfill({ json: { target, targetRevision: 'relay-revision', inventory: { relay: { state: 'active', endpoint: 'http://127.0.0.1:6276/mcp', healthy: true, intentionalPaused: false }, members: [{ id: 'mcp:search', kind: 'mcp', name: 'search', contentHash: 'c'.repeat(64), protected: false, scope: 'user' }, { id: 'anchor:claude', kind: 'anchor', name: 'toolhub-relay', contentHash: 'd'.repeat(64), protected: false, scope: 'user' }, { id: 'anchor:codex', kind: 'anchor', name: 'toolhub-relay', contentHash: 'e'.repeat(64), protected: false, scope: 'user' }, { id: 'mcp:user-extra', kind: 'mcp', name: 'user-extra', contentHash: 'f'.repeat(64), protected: false, scope: 'user' }] }, desired: { snapshot: { id: '44444444-4444-4444-8444-444444444444', revision: 3, sourceKind: 'profile_apply', profileRevision: 7, manifestHash: 'a'.repeat(64), createdAt: now }, manifest: { schemaVersion: 1, target: { id: targetID, nodeId: nodeID, nodeKind: 'local', runtime: 'shared-relay', managedUsername: 'runner' }, profileId: '55555555-5555-4555-8555-555555555555', profileRevision: 7, skills: [], mcpServers: [{ memberId: '66666666-6666-4666-8666-666666666666', serverId: serverID, revision: 2, name: 'search', transport: 'http', url: server.url, contentHash: server.contentHash }], managedMemberIds: ['66666666-6666-4666-8666-666666666666'], relayPort: 6276 } } } })
    }
    if (path === `/api/v1/targets/${targetID}/backups`) return route.fulfill({ json: { items: [] } })
    if (path.startsWith(`/api/v1/targets/${targetID}/relay/`)) {
      relayAction = path.split('/').at(-1) ?? ''
      return route.fulfill({ status: 202, json: { id: '77777777-7777-4777-8777-777777777777', kind: `relay_${relayAction}`, status: 'queued', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    }
    if (path === `/api/v1/mcp/servers/${serverID}` && request.method() === 'PUT') {
      secretUpdate = request.postDataJSON()
      secretIdempotency = request.headers()['idempotency-key'] ?? ''
      return route.fulfill({ json: { ...server, revision: 3, updatedAt: now } })
    }
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/targets')
  await expect(page.getByRole('heading', { name: 'local/shared-relay' })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'search' }).getByText('managed', { exact: true })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'user-extra' }).getByText('unmanaged', { exact: true })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'toolhub-relay' }).getByText('managed', { exact: true })).toHaveCount(2)
  for (const label of ['Start', 'Stop', 'Restart', 'Health check']) await expect(page.getByRole('button', { name: label, exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Health check', exact: true }).click()
  await expect(page.getByText(/Health check queued/)).toBeVisible()
  expect(relayAction).toBe('health')

  await navigate(page, testInfo.project.name, 'MCP')
  await page.getByRole('button', { name: 'Edit' }).click()
  const dialog = page.getByRole('dialog', { name: 'Edit · search' })
  const secret = dialog.getByLabel('Environment secrets API_TOKEN')
  await expect(secret).toHaveAttribute('type', 'password')
  await expect(secret).toHaveValue('')
  await expect(secret).toHaveAttribute('placeholder', 'Unchanged')
  await secret.fill('rotated-browser-fixture')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toHaveCount(0)
  expect(secretUpdate?.env).toEqual({ API_TOKEN: 'rotated-browser-fixture' })
  expect(secretIdempotency).toBeTruthy()
  await expectNoViewportOverflow(page)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-relay-and-secrets.png`, fullPage: true })
})

test('local Skill and MCP intake stays revision-bound and browser-safe', async ({ page }, testInfo) => {
  const targetID = '11111111-1111-4111-8111-111111111111'
  const nodeID = '22222222-2222-4222-8222-222222222222'
  const targetRevision = 'a'.repeat(64)
  const skillHash = 'b'.repeat(64)
  const mcpHash = 'c'.repeat(64)
  const target = { id: targetID, targetKey: 'local/claude', nodeId: nodeID, nodeName: 'local', nodeKind: 'local', runtime: 'claude', managedUsername: 'runner', writable: true, health: 'healthy', desiredRevision: 0, targetRevision, lastScannedAt: now }
  let skillImportBody: Record<string, unknown> | null = null
  let mcpImportBody: Record<string, unknown> | null = null
  let skillIdempotency = ''
  let mcpIdempotency = ''

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/targets') return route.fulfill({ json: { items: [target] } })
    if (path === '/api/v1/skills') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: [] } })
    if (path === `/api/v1/targets/${targetID}`) return route.fulfill({ json: { target, targetRevision, inventory: { members: [{ id: 'skill:formatter', kind: 'skill', name: 'formatter', contentHash: skillHash, protected: false, scope: 'user' }] }, desired: null } })
    if (path === `/api/v1/targets/${targetID}/backups`) return route.fulfill({ json: { items: [] } })
    if (path === `/api/v1/targets/${targetID}/skill-import`) {
      skillImportBody = request.postDataJSON()
      skillIdempotency = request.headers()['idempotency-key'] ?? ''
      return route.fulfill({ status: 202, json: { id: '33333333-3333-4333-8333-333333333333', kind: 'skill_import', status: 'queued', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    }
    if (path === `/api/v1/targets/${targetID}/mcp-import/preflight`) {
      expect(request.postDataJSON()).toEqual({})
      const preview = { targetRevision: 'd'.repeat(64), items: [{ name: 'local-search', transport: 'stdio', command: '/usr/bin/local-search', args: ['--stdio'], envKeys: ['API_TOKEN'], headerKeys: ['Authorization'], contentHash: mcpHash, confirmationToken: 'one-use-local-mcp-confirmation', expiresAt: '2099-01-01T00:05:00Z' }] }
      expect(JSON.stringify(preview)).not.toContain('plaintext-never-in-browser')
      return route.fulfill({ json: preview })
    }
    if (path === '/api/v1/mcp/import') {
      mcpImportBody = request.postDataJSON()
      mcpIdempotency = request.headers()['idempotency-key'] ?? ''
      return route.fulfill({ status: 202, json: { id: '44444444-4444-4444-8444-444444444444', kind: 'mcp_import', status: 'queued', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    }
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/targets')
  await page.getByRole('row').filter({ hasText: 'formatter' }).getByRole('button', { name: 'Import Skill', exact: true }).click()
  await expect(page.getByText(/Skill import queued/)).toBeVisible()
  expect(skillImportBody).toEqual({ name: 'formatter', expectedRevision: targetRevision, contentHash: skillHash })
  expect(skillIdempotency).toBeTruthy()

  await page.getByRole('button', { name: 'Import MCP', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: /Import MCP from runtime/ })
  await expect(dialog).toContainText('API_TOKEN')
  await expect(dialog).toContainText('Authorization')
  await expect(dialog).not.toContainText('plaintext-never-in-browser')
  await dialog.getByRole('radio', { name: 'Select local-search' }).check()
  await dialog.getByRole('checkbox', { name: 'Read and encrypt the selected secret values once' }).check()
  await dialog.getByRole('button', { name: 'Import selected' }).click()
  await expect(page.getByText(/MCP import queued/)).toBeVisible()
  expect(mcpImportBody).toEqual({ confirmationToken: 'one-use-local-mcp-confirmation' })
  expect(mcpIdempotency).toBeTruthy()
  await expectNoViewportOverflow(page)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-local-intake.png`, fullPage: true })
})

test('Salt node managed username override can be set and cleared', async ({ page }) => {
  const targetID = '11111111-1111-4111-8111-111111111111'
  const nodeID = '22222222-2222-4222-8222-222222222222'
  const targetRevision = 'a'.repeat(64)
  const target = { id: targetID, targetKey: 'remote-01/claude', nodeId: nodeID, nodeName: 'remote-01', nodeKind: 'salt', saltMinionId: 'remote-01', runtime: 'claude', managedUsername: 'runner', writable: true, health: 'healthy', desiredRevision: 0, targetRevision }
  let override: string | null = null
  const updates: string[] = []

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/targets') return route.fulfill({ json: { items: [target] } })
    if (path === '/api/v1/skills' || path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/nodes' && request.method() === 'GET') return route.fulfill({ json: { items: [{ id: nodeID, name: 'remote-01', kind: 'salt', saltMinionId: 'remote-01', managedUsernameOverride: override, status: 'online', saltVersion: '3008.0', createdAt: now, updatedAt: now }] } })
    if (path === `/api/v1/nodes/${nodeID}` && request.method() === 'PATCH') {
      const body = request.postDataJSON() as { managedUsername: string }
      updates.push(body.managedUsername)
      override = body.managedUsername || null
      target.managedUsername = override ?? 'runner'
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === `/api/v1/targets/${targetID}`) return route.fulfill({ json: { target, targetRevision, inventory: { members: [] }, desired: null } })
    if (path === `/api/v1/targets/${targetID}/backups`) return route.fulfill({ json: { items: [] } })
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/targets')
  await page.getByRole('button', { name: 'Edit username', exact: true }).click()
  let dialog = page.getByRole('dialog', { name: /Managed username/ })
  await dialog.getByLabel('Node username override').fill('operator')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByText('operator', { exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Edit username', exact: true }).click()
  dialog = page.getByRole('dialog', { name: /Managed username/ })
  await dialog.getByLabel('Node username override').fill('')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByText('runner', { exact: true })).toBeVisible()
  expect(updates).toEqual(['operator', ''])
  await expectNoViewportOverflow(page)
})
