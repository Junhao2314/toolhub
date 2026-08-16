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
  await expect.poll(() => page.evaluate(() => {
    const viewportWidth = window.visualViewport?.width ?? window.innerWidth
    return document.documentElement.scrollWidth <= viewportWidth + 1
  })).toBe(true)
}

function session() {
  return { authenticated: true, csrfToken: 'fixture-csrf', expiresAt: '2099-01-01T00:00:00Z', user: { username: 'admin', passwordChangeRecommended: false } }
}

function compatibilityRelayConfiguration() {
  const revision = { id: 'c1111111-1111-4111-8111-111111111111', revision: 1, canonicalHash: '0'.repeat(64), mcpServers: [], createdAt: now }
  return {
    current: revision,
    applied: revision,
    mode: 'compatibility',
    defaultProfileId: null,
    migration: { state: 'compatibility_ready', pendingContractReviews: 0, ambiguousProfiles: 0, legacyProfileState: 'pending', restorableSnapshot: false },
    runtimeCapability: { compatible: false, features: [], errorCode: 'target_unavailable' },
  }
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

test('Marketplace cards open complete Skill details without hijacking card actions', async ({ page }, testInfo) => {
  const description = '从灵感到成片的全流程AI视频创作助手。支持灵感裂变、专业分镜脚本、剧本诊断优化、爆款公式、多平台适配（即梦/可灵/Runway），并提供完整的发布工作流。'
  const sourceUrl = `https://example.invalid/skills/${'ai-video-creator-'.repeat(10)}`
  const skill = {
    source: 'skillhub',
    id: 'ai-video-creator',
    name: 'AI视频创作师',
    description,
    author: 'ToolHub Studio',
    stars: 89,
    downloads: 1280,
    version: '1.4.0',
    status: 'verified',
    sourceUrl,
  }
  const skillWithoutStatus = {
    source: 'skillsmp',
    id: 'storyboard-assistant',
    name: '分镜助手',
    description: '生成专业分镜。',
    githubUrl: 'https://example.invalid/skills/storyboard-assistant',
  }

  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/market/search') return route.fulfill({ json: { items: [skill, skillWithoutStatus] } })
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/marketplace')
  const card = page.locator('.market-item').filter({ hasText: skill.name })
  const detailsButton = card.getByRole('button', { name: `View details · ${skill.name}` })
  await expect(detailsButton).toBeVisible()

  await card.click()
  let dialog = page.getByRole('dialog', { name: `Skill details · ${skill.name}` })
  let closeButton = dialog.getByRole('button', { name: 'Close' })
  await expect(dialog).toContainText(description)
  await expect(dialog).toContainText('ToolHub Studio')
  await expect(dialog).toContainText('89')
  await expect(dialog).toContainText('1,280')
  await expect(dialog).toContainText(sourceUrl)
  const details = dialog.locator('.detail-list')
  const detailValue = (label: string) => details.getByText(label, { exact: true }).locator('..').locator('dd')
  await expect(detailValue('Source')).toHaveText('skillhub')
  await expect(detailValue('Status')).toHaveText('verified')
  await expect(detailValue('Version')).toHaveText('1.4.0')
  await expect(closeButton).toBeFocused()
  await expectNoViewportOverflow(page)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-marketplace-details.png` })
  await page.keyboard.press('Shift+Tab')
  await expect(dialog.getByRole('button', { name: 'Import' })).toBeFocused()
  await page.keyboard.press('Tab')
  await expect(closeButton).toBeFocused()
  await closeButton.click()
  await expect(dialog).toHaveCount(0)
  await expect(detailsButton).toBeFocused()

  await page.keyboard.press('Space')
  dialog = page.getByRole('dialog', { name: `Skill details · ${skill.name}` })
  await expect(dialog).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  await expect(detailsButton).toBeFocused()

  await page.keyboard.press('Enter')
  dialog = page.getByRole('dialog', { name: `Skill details · ${skill.name}` })
  await expect(dialog).toBeVisible()
  await page.locator('.modal-backdrop').click({ position: { x: 2, y: 2 } })
  await expect(dialog).toHaveCount(0)

  const cardWithoutStatus = page.locator('.market-item').filter({ hasText: skillWithoutStatus.name })
  await cardWithoutStatus.click()
  dialog = page.getByRole('dialog', { name: `Skill details · ${skillWithoutStatus.name}` })
  const missingStatus = dialog.locator('.detail-list').getByText('Status', { exact: true }).locator('..').locator('dd')
  await expect(missingStatus).toHaveText('—')
  await dialog.getByRole('button', { name: 'Close' }).click()

  const popupPromise = page.waitForEvent('popup')
  await card.getByRole('link', { name: 'Open source' }).click()
  const popup = await popupPromise
  await popup.close()
  await expect(page.getByRole('dialog')).toHaveCount(0)

  await card.getByRole('button', { name: 'Import' }).click()
  await expect(page.getByRole('dialog', { name: `Import · ${skill.name}` })).toBeVisible()
})

test('Targets refresh waits for completion and reloads active inventory', async ({ page }) => {
  const operationID = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
  const oldTarget = {
    id: '11111111-1111-4111-8111-111111111111',
    targetKey: 'salt:old-minion/claude',
    nodeId: '22222222-2222-4222-8222-222222222222',
    nodeName: 'old-minion',
    nodeKind: 'salt',
    saltMinionId: 'old-minion',
    runtime: 'claude',
    managedUsername: 'runner',
    writable: true,
    health: 'healthy',
    desiredRevision: 0,
    targetRevision: '',
  }
  const newTarget = {
    ...oldTarget,
    id: '33333333-3333-4333-8333-333333333333',
    targetKey: 'salt:new-minion/claude',
    nodeId: '44444444-4444-4444-8444-444444444444',
    nodeName: 'new-minion',
    saltMinionId: 'new-minion',
  }
  let refreshPosts = 0
  let targetReads = 0
  let operationPolls = 0
  let refreshSucceeded = false
  let releaseSuccess: () => void = () => {}
  const successGate = new Promise<void>((resolve) => { releaseSuccess = resolve })

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/skills' || path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/targets' && request.method() === 'GET') {
      targetReads++
      return route.fulfill({ json: { items: [refreshSucceeded ? newTarget : oldTarget] } })
    }
    if (path === `/api/v1/targets/${oldTarget.id}`) return route.fulfill({ json: { target: oldTarget, targetRevision: '', inventory: { members: [] }, desired: null } })
    if (path === `/api/v1/targets/${newTarget.id}`) return route.fulfill({ json: { target: newTarget, targetRevision: '', inventory: { members: [] }, desired: null } })
    if (path === `/api/v1/targets/${oldTarget.id}/backups` || path === `/api/v1/targets/${newTarget.id}/backups`) return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/nodes/refresh' && request.method() === 'POST') {
      refreshPosts++
      return route.fulfill({ status: 202, json: { id: operationID, kind: 'refresh', status: 'queued', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    }
    if (path === `/api/v1/operations/${operationID}`) {
      operationPolls++
      if (operationPolls === 1) return route.fulfill({ json: { id: operationID, kind: 'refresh', status: 'running', metadata: {}, cancelRequested: false, createdAt: now, startedAt: now, updatedAt: now } })
      await successGate
      refreshSucceeded = true
      return route.fulfill({ json: { id: operationID, kind: 'refresh', status: 'succeeded', metadata: {}, cancelRequested: false, createdAt: now, startedAt: now, finishedAt: now, updatedAt: now } })
    }
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/targets')
  await expect(page.getByText('old-minion', { exact: true }).first()).toBeVisible()
  const refresh = page.getByRole('button', { name: 'Refresh nodes' })
  await page.evaluate(() => {
    const button = [...document.querySelectorAll('button')].find((item) => item.textContent?.includes('Refresh nodes'))
    button?.click()
    button?.click()
  })
  await expect.poll(() => refreshPosts).toBe(1)
  await expect(refresh).toBeDisabled()
  await expect.poll(() => operationPolls).toBeGreaterThanOrEqual(1)
  await expect(page.getByText('old-minion', { exact: true }).first()).toBeVisible()
  releaseSuccess()
  await expect(page.getByText('new-minion', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('old-minion', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Node refresh completed')).toBeVisible()
  expect(refreshPosts).toBe(1)
})

test('Targets refresh surfaces terminal failure without replacing inventory', async ({ page }) => {
  const operationID = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
  const target = {
    id: '55555555-5555-4555-8555-555555555555',
    targetKey: 'salt:stable-minion/claude',
    nodeId: '66666666-6666-4666-8666-666666666666',
    nodeName: 'stable-minion',
    nodeKind: 'salt',
    saltMinionId: 'stable-minion',
    runtime: 'claude',
    managedUsername: 'runner',
    writable: true,
    health: 'healthy',
    desiredRevision: 0,
    targetRevision: '',
  }
  let targetReads = 0

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/skills' || path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/targets' && request.method() === 'GET') {
      targetReads++
      return route.fulfill({ json: { items: [target] } })
    }
    if (path === `/api/v1/targets/${target.id}`) return route.fulfill({ json: { target, targetRevision: '', inventory: { members: [] }, desired: null } })
    if (path === `/api/v1/targets/${target.id}/backups`) return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/nodes/refresh' && request.method() === 'POST') {
      return route.fulfill({ status: 202, json: { id: operationID, kind: 'refresh', status: 'queued', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    }
    if (path === `/api/v1/operations/${operationID}`) {
      return route.fulfill({ json: { id: operationID, kind: 'refresh', status: 'failed', metadata: {}, errorCode: 'target_unavailable', errorReason: 'Could not list accepted Salt keys', cancelRequested: false, createdAt: now, startedAt: now, finishedAt: now, updatedAt: now } })
    }
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/targets')
  await expect(page.getByText('stable-minion', { exact: true }).first()).toBeVisible()
  const initialTargetReads = targetReads
  const refresh = page.getByRole('button', { name: 'Refresh nodes' })
  await refresh.click()
  await expect(page.getByText('Could not list accepted Salt keys')).toBeVisible()
  await expect(page.getByText('stable-minion', { exact: true }).first()).toBeVisible()
  await expect(refresh).toBeEnabled()
  expect(targetReads).toBe(initialTargetReads)
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
    if (path === '/api/v1/relay/contracts') return route.fulfill({ json: { items: [], renames: [] } })
    if (path === '/api/v1/relay/configuration') return route.fulfill({ json: compatibilityRelayConfiguration() })
    if (path === '/api/v1/mcp/policy') return route.fulfill({ json: { current: { id: '99999999-9999-4999-8999-999999999998', revision: 1, canonicalHash: 'b'.repeat(64), catalogVersion: 1, explicitOverrides: {}, unclassifiedMutating: 'confirm', reviewedReadOnly: 'allow', createdAt: now }, applied: { id: '99999999-9999-4999-8999-999999999998', revision: 1, canonicalHash: 'b'.repeat(64), catalogVersion: 1, explicitOverrides: {}, unclassifiedMutating: 'confirm', reviewedReadOnly: 'allow', createdAt: now } } })
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

test('Profile governance pins accepted tools, tightens policy, and gates native launch', async ({ page }, testInfo) => {
  const consoleErrors: string[] = []
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()) })
  const skillID = '11111111-1111-4111-8111-111111111111'
  const skillVersionID = '12111111-1111-4111-8111-111111111111'
  const serverID = '22222222-2222-4222-8222-222222222222'
  const mcpRevisionID = '23222222-2222-4222-8222-222222222222'
  const contractRevisionID = '24222222-2222-4222-8222-222222222222'
  const searchToolID = '25222222-2222-4222-8222-222222222222'
  const writeToolID = '26222222-2222-4222-8222-222222222222'
  const claudeProfileID = '33333333-3333-4333-8333-333333333333'
  const codexProfileID = '34333333-3333-4333-8333-333333333333'
  const claudeRevisionID = '35333333-3333-4333-8333-333333333333'
  const codexRevisionID = '36333333-3333-4333-8333-333333333333'
  const claudeTargetID = '44444444-4444-4444-8444-444444444444'
  const codexTargetID = '45444444-4444-4444-8444-444444444444'
  const relayTargetID = '46444444-4444-4444-8444-444444444444'
  const operationID = '55555555-5555-4555-8555-555555555555'
  const skill = { id: skillID, slug: 'formatter', name: 'Formatter', description: 'Pinned formatter', sourceKind: 'git', currentVersionId: skillVersionID, currentSha256: 'a'.repeat(64), currentContentHash: 'b'.repeat(64), manifest: {}, scanReport: {}, createdAt: now, updatedAt: now }
  const server = { id: serverID, currentRevisionId: mcpRevisionID, name: 'acemcp', description: 'Semantic code search', revision: 4, transport: 'http', args: [], url: 'http://127.0.0.1:8000/mcp', envKeys: [], headerKeys: [], contentHash: 'c'.repeat(64), createdAt: now, updatedAt: now }
  const contract = {
    items: [{
      serverId: serverID,
      serverName: 'acemcp',
      reviewState: 'accepted',
      latest: null,
      accepted: {
        revision: { id: contractRevisionID, serverId: serverID, revision: 2, canonicalHash: 'd'.repeat(64), normalizedContract: {}, createdAt: now },
        tools: [
          { id: searchToolID, serverId: serverID, name: 'acemcp_search_code', position: 0, inputSchema: {}, outputSchema: {}, annotations: { readOnlyHint: true }, presentation: {}, status: 'new_hidden', globalDecision: 'allow', reasonCodes: ['annotation_read_only', 'reviewed_read_only'] },
          { id: writeToolID, serverId: serverID, name: 'acemcp_update_index', position: 1, inputSchema: {}, outputSchema: {}, annotations: { mutatingHint: true }, presentation: {}, status: 'new_hidden', globalDecision: 'confirm', reasonCodes: ['annotation_mutating'] },
        ],
      },
    }],
    renames: [],
  }
  const targets = [
    { id: claudeTargetID, targetKey: 'local/claude', nodeId: '47444444-4444-4444-8444-444444444444', nodeName: 'local', nodeKind: 'local', runtime: 'claude', managedUsername: 'runner', writable: true, health: 'healthy', desiredRevision: 0, targetRevision: 'claude-target' },
    { id: codexTargetID, targetKey: 'local/codex', nodeId: '47444444-4444-4444-8444-444444444444', nodeName: 'local', nodeKind: 'local', runtime: 'codex', managedUsername: 'runner', writable: true, health: 'healthy', desiredRevision: 0, targetRevision: 'codex-target' },
    { id: relayTargetID, targetKey: 'local/shared-relay', nodeId: '47444444-4444-4444-8444-444444444444', nodeName: 'local', nodeKind: 'local', runtime: 'shared-relay', managedUsername: 'runner', writable: true, health: 'healthy', desiredRevision: 0, targetRevision: 'relay-target' },
  ]
  const profiles: Record<string, unknown>[] = []
  const savedBodies: Record<string, unknown>[] = []

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/skills') return route.fulfill({ json: { items: [skill] } })
    if (path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: [server] } })
    if (path === '/api/v1/relay/contracts') return route.fulfill({ json: contract })
    if (path === '/api/v1/mcp/policy') return route.fulfill({ json: { current: { id: '57555555-5555-4555-8555-555555555555', revision: 1, canonicalHash: 'e'.repeat(64), catalogVersion: 1, explicitOverrides: { [searchToolID]: 'allow', [writeToolID]: 'confirm' }, unclassifiedMutating: 'confirm', reviewedReadOnly: 'allow', createdAt: now }, applied: { id: '57555555-5555-4555-8555-555555555555', revision: 1, canonicalHash: 'e'.repeat(64), catalogVersion: 1, explicitOverrides: { [searchToolID]: 'allow', [writeToolID]: 'confirm' }, unclassifiedMutating: 'confirm', reviewedReadOnly: 'allow', createdAt: now } } })
    if (path === '/api/v1/relay/configuration') return route.fulfill({ json: compatibilityRelayConfiguration() })
    if (path === '/api/v1/targets') return route.fulfill({ json: { items: targets } })
    if (path === '/api/v1/profiles' && request.method() === 'GET') return route.fulfill({ json: { items: profiles } })
    if (path === '/api/v1/profiles' && request.method() === 'POST') {
      const body = request.postDataJSON() as Record<string, unknown>
      savedBodies.push(body)
      const isClaude = body.clientKind === 'claude'
      const profile = {
        ...body,
        id: isClaude ? claudeProfileID : codexProfileID,
        currentRevisionId: isClaude ? claudeRevisionID : codexRevisionID,
        revision: 1,
        migrationState: 'ready',
        canonicalHash: 'f'.repeat(64),
        pendingBindings: false,
        skills: [],
        mcpServers: [],
        effectiveVisibleCount: 1,
        createdAt: now,
        updatedAt: now,
      }
      profiles.push(profile)
      return route.fulfill({ status: 201, json: profile })
    }
    if (path.endsWith('/preflight')) {
      const profileID = path.split('/')[4]
      const expectedTargetID = profileID === claudeProfileID ? claudeTargetID : codexTargetID
      expect(request.postDataJSON()).toEqual({ targetIds: [expectedTargetID, relayTargetID] })
      return route.fulfill({ json: { items: [expectedTargetID, relayTargetID].map((targetId) => ({ targetId, confirmationToken: `confirm-${targetId}`, expiresAt: '2099-01-01T00:05:00Z', result: { targetRevision: 'target-revision', manifestHash: '1'.repeat(64), diff: { add: [], replace: [], delete: [], excluded: [] } } })) } })
    }
    if (path.endsWith('/apply')) {
      const body = request.postDataJSON() as { confirmationTokens: string[] }
      expect(body.confirmationTokens).toHaveLength(2)
      const profileID = path.split('/')[4]
      const profile = profiles.find((item) => item.id === profileID)
      if (profile) {
        profile.publishedRevisionId = profile.currentRevisionId
        profile.publishedRevision = profile.revision
        profile.publishedAt = now
      }
      return route.fulfill({ status: 202, json: { id: operationID, kind: 'apply', status: 'succeeded', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    }
    if (path === `/api/v1/profiles/${claudeProfileID}/launch`) return route.fulfill({ json: { ready: true, profileId: claudeProfileID, profileRevisionId: claudeRevisionID, clientKind: 'claude', nativeClient: { clientKind: 'claude', version: '1.0.90', supported: true }, command: { executable: 'claude', args: ['--mcp-config', 'toolhub'], display: 'claude --mcp-config toolhub' } } })
    if (path === `/api/v1/profiles/${codexProfileID}/launch`) return route.fulfill({ json: { ready: false, reasonCode: 'relay_routing_mismatch', profileId: codexProfileID, profileRevisionId: codexRevisionID, clientKind: 'codex', nativeClient: { clientKind: 'codex', version: '0.85.0', supported: true } } })
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/profiles')
  for (const [name, clientLabel] of [['claude-coding', 'Claude'], ['codex-coding', 'Codex']] as const) {
    await page.getByRole('button', { name: 'New Profile' }).click()
    const editor = page.getByRole('dialog', { name: 'New Profile' })
    for (const section of ['Basic information', 'Skills', 'MCP tools', 'Tool rules', 'Application status']) await expect(editor.getByRole('tab', { name: section })).toBeVisible()
    await editor.getByLabel('Name').fill(name)
    await editor.getByLabel('Client').selectOption({ label: clientLabel })
    await editor.getByLabel('Category').fill('coding')
    await editor.getByLabel('Variant').fill('default')
    await editor.getByRole('tab', { name: 'Skills' }).click()
    await editor.getByRole('checkbox', { name: /Formatter/ }).check()
    await editor.getByRole('tab', { name: 'MCP tools' }).click()
    await editor.getByRole('checkbox', { name: /acemcp/ }).check()
    await editor.getByLabel('Visibility · acemcp').selectOption(name === 'claude-coding' ? 'selected' : 'all_accepted')
    await editor.getByRole('button', { name: 'Configure tools · acemcp' }).click()
    if (name === 'claude-coding') await editor.getByRole('checkbox', { name: 'Visible · acemcp_search_code' }).check()
    await editor.getByRole('tab', { name: 'Tool rules' }).click()
    const mutatingDecision = editor.getByLabel('Decision · acemcp_update_index')
    await expect(mutatingDecision.locator('option[value="allow"]')).toHaveCount(0)
    if (name === 'claude-coding') await editor.getByLabel('Decision · acemcp_search_code').selectOption('confirm')
    await editor.getByRole('tab', { name: 'Application status' }).click()
    await expect(editor.getByText('Draft', { exact: true })).toBeVisible()
    await expect(editor.getByText(new RegExp(`${name === 'claude-coding' ? 1 : 0} visible tools?`))).toBeVisible()
    if (name === 'claude-coding') await page.screenshot({ path: `../test-results/${testInfo.project.name}-profile-governance-editor.png`, fullPage: true })
    await editor.getByRole('button', { name: 'Save' }).click()
    await expect(editor).toHaveCount(0)
  }

  expect(savedBodies).toHaveLength(2)
  expect(savedBodies[0]).toMatchObject({
    name: 'claude-coding',
    clientKind: 'claude',
    category: 'coding',
    variant: 'default',
    skillIds: [skillID],
    mcpServerIds: [serverID],
    skillVersionIds: { [skillID]: skillVersionID },
    mcpRevisionIds: { [serverID]: mcpRevisionID },
    mcpGovernance: [{ serverId: serverID, mcpRevisionId: mcpRevisionID, acceptedContractRevisionId: contractRevisionID, visibilityMode: 'selected' }],
  })
  expect(savedBodies[0].toolRules).toEqual(expect.arrayContaining([expect.objectContaining({ toolId: searchToolID, visible: true, decision: 'confirm' })]))
  await expect(page.getByRole('row').filter({ hasText: 'claude-coding' })).toContainText('Draft')

  const claudeRow = page.getByRole('row').filter({ hasText: 'claude-coding' })
  await claudeRow.getByRole('button', { name: 'Preflight and Apply' }).click()
  const applyDialog = page.getByRole('dialog', { name: 'Apply Profile · claude-coding' })
  await applyDialog.getByRole('checkbox', { name: /local\/claude/ }).check()
  await applyDialog.getByRole('checkbox', { name: /local\/shared-relay/ }).check()
  await applyDialog.getByRole('button', { name: 'Run preflight' }).click()
  await applyDialog.getByRole('button', { name: 'Confirm Apply' }).click()
  await expect(page.getByText(/Apply queued/)).toBeVisible()

  await claudeRow.click()
  await page.getByRole('button', { name: 'Launch session' }).click()
  let launchDialog = page.getByRole('dialog', { name: 'Launch session · claude-coding' })
  await expect(launchDialog.getByText('claude --mcp-config toolhub')).toBeVisible()
  await expect(launchDialog.getByRole('button', { name: 'Copy launch command' })).toBeVisible()
  await launchDialog.getByRole('button', { name: 'Close' }).last().click()

  await page.getByRole('row').filter({ hasText: 'codex-coding' }).click()
  await page.getByRole('button', { name: 'Launch session' }).click()
  launchDialog = page.getByRole('dialog', { name: 'Launch session · codex-coding' })
  await expect(launchDialog.getByText('Configuration mismatch')).toBeVisible()
  await expect(launchDialog.getByRole('button', { name: 'Copy launch command' })).toHaveCount(0)
  await expect(launchDialog).not.toContainText('codex --')
  await expectNoViewportOverflow(page)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-profile-governance.png`, fullPage: true })
  expect(consoleErrors).toEqual([])
})

test('editing a Profile preserves historical pins until explicit contract adoption', async ({ page }) => {
  const profileID = '61333333-3333-4333-8333-333333333333'
  const profileRevisionID = '62333333-3333-4333-8333-333333333333'
  const skillID = '61111111-1111-4111-8111-111111111111'
  const pinnedSkillVersionID = '62111111-1111-4111-8111-111111111111'
  const currentSkillVersionID = '63111111-1111-4111-8111-111111111111'
  const serverID = '61222222-2222-4222-8222-222222222222'
  const pinnedMCPRevisionID = '62222222-2222-4222-8222-222222222222'
  const currentMCPRevisionID = '63222222-2222-4222-8222-222222222222'
  const pinnedContractRevisionID = '64222222-2222-4222-8222-222222222222'
  const acceptedContractRevisionID = '65222222-2222-4222-8222-222222222222'
  const toolID = '66222222-2222-4222-8222-222222222222'
  const removedToolID = '67222222-2222-4222-8222-222222222222'
  const profile = {
    id: profileID,
    currentRevisionId: profileRevisionID,
    publishedRevisionId: profileRevisionID,
    publishedRevision: 4,
    publishedAt: now,
    name: 'claude-historical',
    description: 'Pinned editing fixture',
    clientKind: 'claude',
    category: 'coding',
    variant: 'default',
    migrationState: 'ready',
    revision: 4,
    canonicalHash: 'a'.repeat(64),
    pendingBindings: false,
    skillIds: [skillID],
    mcpServerIds: [serverID],
    skills: [{ skillId: skillID, versionId: pinnedSkillVersionID, slug: 'formatter', name: 'Formatter', sha256: 'b'.repeat(64), contentHash: 'c'.repeat(64), current: false }],
    mcpServers: [{ serverId: serverID, revisionId: pinnedMCPRevisionID, revision: 3, name: 'acemcp', transport: 'http', url: 'http://127.0.0.1:8000/mcp', envKeys: [], headerKeys: [], contentHash: 'd'.repeat(64), current: false }],
    mcpGovernance: [{ serverId: serverID, mcpRevisionId: pinnedMCPRevisionID, acceptedContractRevisionId: pinnedContractRevisionID, visibilityMode: 'all_accepted' }],
    toolRules: [
      { toolId: toolID, visible: true, decision: 'allow', reasonCodes: [] },
      { toolId: removedToolID, visible: true, decision: 'confirm', reasonCodes: ['profile-rule'] },
    ],
    effectiveVisibleCount: 1,
    createdAt: now,
    updatedAt: now,
  }
  const skill = { id: skillID, slug: 'formatter', name: 'Formatter', description: 'Current formatter', sourceKind: 'git', currentVersionId: currentSkillVersionID, currentSha256: 'e'.repeat(64), currentContentHash: 'f'.repeat(64), manifest: {}, scanReport: {}, createdAt: now, updatedAt: now }
  const server = { id: serverID, currentRevisionId: currentMCPRevisionID, name: 'acemcp', description: 'Semantic code search', revision: 8, transport: 'http', args: [], url: 'http://127.0.0.1:8000/mcp', envKeys: [], headerKeys: [], contentHash: '1'.repeat(64), createdAt: now, updatedAt: now }
  const contract = {
    items: [{
      serverId: serverID,
      serverName: 'acemcp',
      reviewState: 'accepted',
      latest: null,
      accepted: {
        revision: { id: acceptedContractRevisionID, serverId: serverID, revision: 7, canonicalHash: '2'.repeat(64), normalizedContract: {}, createdAt: now },
        tools: [{ id: toolID, serverId: serverID, name: 'acemcp_update_index', position: 0, inputSchema: {}, outputSchema: {}, annotations: { mutatingHint: true }, presentation: {}, status: 'unchanged', globalDecision: 'confirm', reasonCodes: ['annotation_mutating'] }],
      },
    }],
    renames: [],
  }
  let savedBody: Record<string, unknown> | null = null

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/profiles' && request.method() === 'GET') return route.fulfill({ json: { items: [profile] } })
    if (path === `/api/v1/profiles/${profileID}` && request.method() === 'PUT') {
      savedBody = request.postDataJSON() as Record<string, unknown>
      return route.fulfill({ json: { ...profile, ...savedBody, currentRevisionId: '68333333-3333-4333-8333-333333333333', revision: 5, updatedAt: now } })
    }
    if (path === '/api/v1/skills') return route.fulfill({ json: { items: [skill] } })
    if (path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: [server] } })
    if (path === '/api/v1/targets') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/relay/contracts') return route.fulfill({ json: contract })
    if (path === '/api/v1/mcp/policy') return route.fulfill({ json: { current: { id: '69555555-5555-4555-8555-555555555555', revision: 2, canonicalHash: '3'.repeat(64), catalogVersion: 1, explicitOverrides: {}, unclassifiedMutating: 'confirm', reviewedReadOnly: 'allow', createdAt: now }, applied: { id: '69555555-5555-4555-8555-555555555555', revision: 2, canonicalHash: '3'.repeat(64), catalogVersion: 1, explicitOverrides: {}, unclassifiedMutating: 'confirm', reviewedReadOnly: 'allow', createdAt: now } } })
    if (path === '/api/v1/relay/configuration') return route.fulfill({ json: compatibilityRelayConfiguration() })
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/profiles')
  const row = page.getByRole('row').filter({ hasText: 'claude-historical' })
  await row.getByRole('button', { name: 'Edit' }).click()
  const editor = page.getByRole('dialog', { name: 'Edit · claude-historical' })

  await editor.getByRole('tab', { name: 'Skills' }).click()
  await expect(editor.getByRole('checkbox', { name: /Formatter/ }).locator('..')).toContainText(pinnedSkillVersionID.slice(0, 12))

  await editor.getByRole('tab', { name: 'MCP tools' }).click()
  await expect(editor.getByText(/r3/)).toBeVisible()
  await expect(editor.getByText('Tool definition version', { exact: true })).toBeVisible()
  await expect(editor.getByRole('button', { name: 'Adopt accepted tool definition version · acemcp' })).toBeVisible()
  await expect(editor.getByRole('button', { name: 'Save' })).toBeDisabled()

  await editor.getByRole('tab', { name: 'Tool rules' }).click()
  await expect(editor.getByLabel('Decision · acemcp_update_index')).toHaveCount(0)

  await editor.getByRole('tab', { name: 'Application status' }).click()
  await expect(editor.getByText('Draft', { exact: true })).toBeVisible()

  await editor.getByRole('tab', { name: 'MCP tools' }).click()
  await editor.getByRole('button', { name: 'Adopt accepted tool definition version · acemcp' }).click()
  await editor.getByLabel('Visibility · acemcp').selectOption('hidden')
  await editor.getByRole('tab', { name: 'Tool rules' }).click()
  const decision = editor.getByLabel('Decision · acemcp_update_index')
  await expect(decision).toHaveValue('confirm')
  await expect(decision.locator('option[value="allow"]')).toHaveCount(0)
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(editor).toHaveCount(0)

  expect(savedBody).toMatchObject({
    revision: 4,
    skillVersionIds: { [skillID]: pinnedSkillVersionID },
    mcpRevisionIds: { [serverID]: pinnedMCPRevisionID },
    mcpGovernance: [{ serverId: serverID, mcpRevisionId: pinnedMCPRevisionID, acceptedContractRevisionId: acceptedContractRevisionID, visibilityMode: 'hidden' }],
    toolRules: [{ toolId: toolID, visible: false, decision: 'confirm', reasonCodes: [] }],
  })
})

test('editing a pre-contract Profile requires adoption and honors confirmed renames', async ({ page }) => {
  const profileID = '71333333-3333-4333-8333-333333333333'
  const profileRevisionID = '72333333-3333-4333-8333-333333333333'
  const serverID = '71222222-2222-4222-8222-222222222222'
  const mcpRevisionID = '72222222-2222-4222-8222-222222222222'
  const acceptedContractRevisionID = '73222222-2222-4222-8222-222222222222'
  const toolID = '74222222-2222-4222-8222-222222222222'
  const removedToolID = '75222222-2222-4222-8222-222222222222'
  const profile = {
    id: profileID,
    currentRevisionId: profileRevisionID,
    name: 'claude-pre-contract',
    description: 'Created before contract review',
    clientKind: 'claude',
    category: 'coding',
    variant: 'default',
    migrationState: 'compatibility',
    revision: 1,
    canonicalHash: '7'.repeat(64),
    pendingBindings: false,
    skillIds: [],
    mcpServerIds: [serverID],
    skills: [],
    mcpServers: [{ serverId: serverID, revisionId: mcpRevisionID, revision: 1, name: 'pre-contract-server', transport: 'http', url: 'http://127.0.0.1:8001/mcp', envKeys: [], headerKeys: [], contentHash: '8'.repeat(64), current: true }],
    mcpGovernance: [{ serverId: serverID, mcpRevisionId: mcpRevisionID, visibilityMode: 'all_accepted' }],
    toolRules: [],
    effectiveVisibleCount: 0,
    createdAt: now,
    updatedAt: now,
  }
  const server = { id: serverID, currentRevisionId: mcpRevisionID, name: 'pre-contract-server', description: '', revision: 1, transport: 'http', args: [], url: 'http://127.0.0.1:8001/mcp', envKeys: [], headerKeys: [], contentHash: '8'.repeat(64), createdAt: now, updatedAt: now }
  const contracts = {
    items: [{
      serverId: serverID,
      serverName: server.name,
      reviewState: 'accepted',
      latest: null,
      accepted: {
        revision: { id: acceptedContractRevisionID, serverId: serverID, revision: 1, canonicalHash: '9'.repeat(64), normalizedContract: {}, createdAt: now },
        tools: [{ id: toolID, serverId: serverID, name: 'read_item', position: 0, inputSchema: {}, outputSchema: {}, annotations: { readOnlyHint: true }, presentation: {}, status: 'new_hidden', globalDecision: 'allow', reasonCodes: ['annotation_read_only', 'reviewed_read_only'] }],
      },
    }],
    renames: [{ id: '76222222-2222-4222-8222-222222222222', serverId: serverID, removedToolId: removedToolID, removedToolName: 'lookup_item', addedToolId: toolID, addedToolName: 'read_item', removedContractRevisionId: '77222222-2222-4222-8222-222222222222', addedContractRevisionId: acceptedContractRevisionID, status: 'confirmed', createdAt: now }],
  }
  let savedBody: Record<string, unknown> | null = null

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/profiles' && request.method() === 'GET') return route.fulfill({ json: { items: [profile] } })
    if (path === `/api/v1/profiles/${profileID}` && request.method() === 'PUT') {
      savedBody = request.postDataJSON() as Record<string, unknown>
      return route.fulfill({ json: { ...profile, ...savedBody, currentRevisionId: '75333333-3333-4333-8333-333333333333', revision: 2, updatedAt: now } })
    }
    if (path === '/api/v1/skills') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: [server] } })
    if (path === '/api/v1/targets') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/relay/contracts') return route.fulfill({ json: contracts })
    if (path === '/api/v1/relay/configuration') return route.fulfill({ json: compatibilityRelayConfiguration() })
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/profiles')
  await page.getByRole('row').filter({ hasText: profile.name }).getByRole('button', { name: 'Edit' }).click()
  const editor = page.getByRole('dialog', { name: `Edit · ${profile.name}` })
  await editor.getByRole('tab', { name: 'MCP tools' }).click()
  const adopt = editor.getByRole('button', { name: `Adopt accepted tool definition version · ${server.name}` })
  await expect(adopt).toBeVisible()
  await expect(editor.getByRole('button', { name: 'Save' })).toBeDisabled()
  await adopt.click()
  await expect(adopt).toHaveCount(0)
  await expect(editor.getByRole('button', { name: 'Save' })).toBeEnabled()
  await editor.getByRole('button', { name: `Configure tools · ${server.name}` }).click()
  await expect(editor.getByRole('checkbox', { name: 'Visible · read_item' })).toBeChecked()
  await editor.getByRole('tab', { name: 'Application status' }).click()
  await expect(editor.getByText('1 visible tool', { exact: true })).toBeVisible()
  await editor.getByRole('button', { name: 'Save' }).click()
  await expect(editor).toHaveCount(0)
  expect(savedBody).toMatchObject({
    mcpGovernance: [{ serverId: serverID, mcpRevisionId: mcpRevisionID, acceptedContractRevisionId: acceptedContractRevisionID, visibilityMode: 'all_accepted' }],
  })
})

test('shared relay governance console reviews updates without exposing payloads', async ({ page }, testInfo) => {
  const consoleErrors: string[] = []
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()) })
  const firstServerID = '81222222-2222-4222-8222-222222222222'
  const secondServerID = '82222222-2222-4222-8222-222222222222'
  const firstMCPRevisionID = '83222222-2222-4222-8222-222222222222'
  const secondMCPRevisionID = '84222222-2222-4222-8222-222222222222'
  const latestSecondMCPRevisionID = '8e222222-2222-4222-8222-222222222222'
  const appliedConfigurationID = '85222222-2222-4222-8222-222222222222'
  const draftConfigurationID = '86222222-2222-4222-8222-222222222222'
  const savedDraftConfigurationID = '8f222222-2222-4222-8222-222222222222'
  const acceptedContractID = '87222222-2222-4222-8222-222222222222'
  const latestContractID = '88222222-2222-4222-8222-222222222222'
  const stableToolID = '89222222-2222-4222-8222-222222222222'
  const removedToolID = '8a222222-2222-4222-8222-222222222222'
  const newToolID = '8b222222-2222-4222-8222-222222222222'
  const renameID = '8c222222-2222-4222-8222-222222222222'
  const ambiguousRenameID = '8d222222-2222-4222-8222-222222222222'
  const profileID = '81333333-3333-4333-8333-333333333333'
  const profileRevisionID = '82333333-3333-4333-8333-333333333333'
  const relayTargetID = '81444444-4444-4444-8444-444444444444'
  const operationID = '81555555-5555-4555-8555-555555555555'
  const challengeID = 'a'.repeat(64)
  const bindingHash = 'b'.repeat(64)
  const argumentHash = 'c'.repeat(64)
  const targetRevision = 'd'.repeat(64)
  const routingHash = 'e'.repeat(64)
  const manifestHash = 'f'.repeat(64)
  const servers = [
    { id: firstServerID, currentRevisionId: firstMCPRevisionID, name: 'acemcp', description: 'Semantic code search', revision: 4, transport: 'http', args: [], url: 'http://127.0.0.1:8000/mcp', envKeys: [], headerKeys: [], contentHash: '1'.repeat(64), createdAt: now, updatedAt: now },
    { id: secondServerID, currentRevisionId: latestSecondMCPRevisionID, name: 'docs', description: 'Documentation search', revision: 3, transport: 'http', args: [], url: 'http://127.0.0.1:8002/mcp', envKeys: [], headerKeys: [], contentHash: '2'.repeat(64), createdAt: now, updatedAt: now },
  ]
  const profile = { id: profileID, currentRevisionId: profileRevisionID, publishedRevisionId: profileRevisionID, publishedRevision: 3, publishedAt: now, name: 'claude-coding', description: '', clientKind: 'claude', category: 'coding', variant: 'default', migrationState: 'ready', revision: 3, canonicalHash: '3'.repeat(64), pendingBindings: false, skillIds: [], mcpServerIds: [firstServerID], skills: [], mcpServers: [], mcpGovernance: [], toolRules: [], effectiveVisibleCount: 1, createdAt: now, updatedAt: now }
  const configuration = {
    current: { id: draftConfigurationID, revision: 2, canonicalHash: '4'.repeat(64), mcpServers: [{ serverId: firstServerID, mcpRevisionId: firstMCPRevisionID, position: 0 }, { serverId: secondServerID, mcpRevisionId: secondMCPRevisionID, position: 1 }], metadata: { portChanged: false }, createdAt: now },
    applied: { id: appliedConfigurationID, revision: 1, canonicalHash: '5'.repeat(64), mcpServers: [{ serverId: firstServerID, mcpRevisionId: firstMCPRevisionID, position: 0 }], metadata: {}, createdAt: now },
    mode: 'compatibility',
    defaultProfileId: null,
    migration: { state: 'waiting_contract_review', pendingContractReviews: 1, ambiguousProfiles: 2, legacyProfileId: '', legacyProfileState: 'pending', restorableSnapshot: false },
    runtimeCapability: { compatible: true, runtimeVersion: '2.15.0-toolhub.1', features: ['profile-session-binding', 'tool-filtering', 'call-policy', 'one-shot-confirmation', 'payload-free-observations', 'routing-hot-reload'] },
  }
  const contracts = {
    items: [{
      serverId: firstServerID,
      serverName: 'acemcp',
      reviewState: 'changed',
      latest: {
        revision: { id: latestContractID, serverId: firstServerID, revision: 2, canonicalHash: '6'.repeat(64), normalizedContract: {}, createdAt: now },
        tools: [
          { id: stableToolID, serverId: firstServerID, name: 'search_code', position: 0, inputSchema: {}, outputSchema: {}, annotations: { readOnlyHint: true }, presentation: { title: 'Search code' }, status: 'changed_presentation', globalDecision: 'allow', reasonCodes: ['annotation_read_only', 'reviewed_read_only'] },
          { id: newToolID, serverId: firstServerID, name: 'lookup_code', position: 1, inputSchema: {}, outputSchema: {}, annotations: { readOnlyHint: true }, presentation: {}, status: 'new_hidden', globalDecision: 'allow', reasonCodes: ['annotation_read_only', 'reviewed_read_only'] },
        ],
      },
      accepted: {
        revision: { id: acceptedContractID, serverId: firstServerID, revision: 1, canonicalHash: '7'.repeat(64), normalizedContract: {}, createdAt: now },
        tools: [
          { id: stableToolID, serverId: firstServerID, name: 'search_code', position: 0, inputSchema: {}, outputSchema: {}, annotations: { readOnlyHint: true }, presentation: {}, status: 'unchanged', globalDecision: 'allow', reasonCodes: ['annotation_read_only', 'reviewed_read_only'] },
          { id: removedToolID, serverId: firstServerID, name: 'find_code', position: 1, inputSchema: {}, outputSchema: {}, annotations: { readOnlyHint: true }, presentation: {}, status: 'unchanged', globalDecision: 'allow', reasonCodes: ['annotation_read_only', 'reviewed_read_only'] },
        ],
      },
    }],
    renames: [
      { id: renameID, serverId: firstServerID, removedToolId: removedToolID, removedToolName: 'find_code', addedToolId: newToolID, addedToolName: 'lookup_code', removedContractRevisionId: acceptedContractID, addedContractRevisionId: latestContractID, status: 'suspected', createdAt: now },
      { id: ambiguousRenameID, serverId: firstServerID, removedToolId: stableToolID, removedToolName: 'search_code', addedToolId: newToolID, addedToolName: 'lookup_code', removedContractRevisionId: acceptedContractID, addedContractRevisionId: latestContractID, status: 'ambiguous', createdAt: now },
    ],
  }
  const relayTarget = { id: relayTargetID, targetKey: 'local/shared-relay', nodeId: '81666666-6666-4666-8666-666666666666', nodeName: 'local', nodeKind: 'local', runtime: 'shared-relay', managedUsername: 'root', writable: true, health: 'healthy', desiredRevision: 1, targetRevision, driftSummary: {}, relayFailureCount: 0, relaySuspended: false, relayMemberStatuses: [{ memberId: firstMCPRevisionID, name: 'acemcp', status: 'ready', capabilityKinds: ['tools'], capabilities: { tools: 2, resources: 0, resourceTemplates: 0, prompts: 0 }, checkedAt: now }, { memberId: secondMCPRevisionID, name: 'docs', status: 'unavailable', capabilityKinds: [], capabilities: { tools: 0, resources: 0, resourceTemplates: 0, prompts: 0 }, checkedAt: now, errorCode: 'upstream_unavailable', errorReason: 'connection refused' }] }
  const confirmations = { items: [{ challengeId: challengeID, bindingHash, argumentHash, createdAt: Date.now() / 1000 - 30, expiresAt: Date.now() / 1000 + 240, profileId: profileID, profileRevisionId: profileRevisionID, profileName: profile.name, clientKind: 'claude', serverId: firstServerID, serverName: 'acemcp', toolId: stableToolID, toolName: 'search_code', runtimeName: 'mcpm', mcpConfigRevisionId: firstMCPRevisionID, contractRevisionId: acceptedContractID, globalPolicyRevisionId: '81777777-7777-4777-8777-777777777777', decision: 'confirm', reasonCodes: ['profile_rule'], argumentSummary: [{ pointer: '/o0', valueType: 'string', arrayLength: null, stringLength: 17, sensitive: true }] }] }
  const live = { bootId: '81888888-8888-4888-8888-888888888888', nextSequence: 2, items: [{ bootId: '81888888-8888-4888-8888-888888888888', sequence: 1, observedAt: Date.now() / 1000 - 10, minuteBucket: now, profileId: profileID, profileRevisionId: profileRevisionID, serverId: firstServerID, toolId: stableToolID, decision: 'confirm', reasonCodes: ['profile_rule'], outcome: 'confirmed', errorClass: 'none', durationBucket: 'lt_100ms' }, { bootId: '81888888-8888-4888-8888-888888888888', sequence: 2, observedAt: Date.now() / 1000 - 5, minuteBucket: now, profileId: profileID, profileRevisionId: profileRevisionID, serverId: firstServerID, toolId: stableToolID, decision: 'confirm', reasonCodes: ['profile_rule'], outcome: 'unknown', errorClass: 'transport', durationBucket: 'lt_10s' }] }
  const daily = { days: 30, items: [{ day: '2026-08-16', profileId: profileID, profileRevisionId: profileRevisionID, serverId: firstServerID, toolId: stableToolID, clientKind: 'claude', decision: 'confirm', outcome: 'executed', errorClass: 'none', callCount: 12, errorCount: 0, durationBucket: 'lt_100ms' }] }
  let prepareBody: unknown = null
  let preflightBody: unknown = null
  let applyBody: unknown = null
  let relayDraftBody: Record<string, unknown> | null = null
  let confirmationBody: unknown = null
  let renameConfirmed = false
  let ambiguousRenameConfirmed = false

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: servers } })
    if (path === '/api/v1/profiles') return route.fulfill({ json: { items: [profile] } })
    if (path === '/api/v1/targets') return route.fulfill({ json: { items: [relayTarget] } })
    if (path === '/api/v1/relay/configuration' && request.method() === 'GET') return route.fulfill({ json: configuration })
    if (path === '/api/v1/relay/configuration' && request.method() === 'PUT') {
      relayDraftBody = request.postDataJSON() as Record<string, unknown>
      configuration.current = {
        ...configuration.current,
        id: savedDraftConfigurationID,
        revision: 3,
        canonicalHash: '8'.repeat(64),
        mcpServers: [{ serverId: firstServerID, mcpRevisionId: firstMCPRevisionID, position: 0 }, { serverId: secondServerID, mcpRevisionId: latestSecondMCPRevisionID, position: 1 }],
      }
      return route.fulfill({ json: configuration.current })
    }
    if (path === '/api/v1/relay/contracts') return route.fulfill({ json: contracts })
    if (path === '/api/v1/relay/confirmations' && request.method() === 'GET') return route.fulfill({ json: confirmations })
    if (path === '/api/v1/relay/observations/live') return route.fulfill({ json: live })
    if (path === '/api/v1/relay/observations/daily') return route.fulfill({ json: daily })
    if (path === '/api/v1/relay/configuration/prepare-profile-updates') {
      prepareBody = request.postDataJSON()
      return route.fulfill({ json: { items: [profileID] } })
    }
    if (path === '/api/v1/relay/configuration/preflight') {
      preflightBody = request.postDataJSON()
      return route.fulfill({ json: { revisionId: draftConfigurationID, routingHash, result: { targetRevision, manifestHash, diff: { add: [{ kind: 'mcp', name: 'docs', afterHash: '9'.repeat(64) }], replace: [], delete: [], excluded: [] } } } })
    }
    if (path === '/api/v1/relay/configuration/apply') {
      applyBody = request.postDataJSON()
      return route.fulfill({ status: 202, json: { id: operationID, kind: 'relay_config_apply', status: 'queued', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    }
    if (path === `/api/v1/relay/renames/${renameID}/confirm`) {
      renameConfirmed = true
      return route.fulfill({ status: 204 })
    }
    if (path === `/api/v1/relay/renames/${ambiguousRenameID}/confirm`) {
      ambiguousRenameConfirmed = true
      return route.fulfill({ status: 204 })
    }
    if (path === `/api/v1/relay/contracts/${latestContractID}/accept`) return route.fulfill({ status: 204 })
    if (path === `/api/v1/relay/confirmations/${challengeID}/approve`) {
      confirmationBody = request.postDataJSON()
      return route.fulfill({ json: { challengeId: challengeID, bindingHash, grantExpiresAt: Date.now() / 1000 + 60 } })
    }
    if (path === '/api/v1/relay/contracts/observe') return route.fulfill({ status: 202, json: { id: operationID, kind: 'contract_observe', status: 'queued', metadata: {}, cancelRequested: false, createdAt: now, updatedAt: now } })
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: `${request.method()} ${path}` } } })
  })

  await page.goto('/mcp')
  await expect(page.getByRole('button', { name: 'MCP services' })).toBeVisible()
  await page.getByRole('button', { name: 'Shared relay' }).click()
  await expect(page.getByRole('heading', { name: 'Shared relay' })).toBeVisible()
  await expect(page.getByText('Configuration r2', { exact: true })).toBeVisible()
  await expect(page.getByText('Applied r1', { exact: true })).toBeVisible()
  await expect(page.getByText('Will disconnect current sessions')).toBeVisible()
  await expect(page.getByText('Waiting for tool definition review', { exact: true })).toBeVisible()
  await expect(page.getByText('2 Profiles need metadata review', { exact: true })).toBeVisible()
  await expect(page.getByText('mcpm 2.15.0-toolhub.1', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Compatibility', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Enforced', exact: true })).toBeVisible()
  const runningSet = page.getByRole('table', { name: 'Running MCP set' })
  await expect(runningSet.getByRole('row').filter({ hasText: 'acemcp' })).toContainText('ready')
  await expect(runningSet.getByRole('row').filter({ hasText: 'docs' })).toHaveCount(0)

  const relayConfiguration = page.getByRole('region', { name: 'Relay configuration' })
  await relayConfiguration.getByText('Edit running set', { exact: true }).click()
  const saveRelayDraft = relayConfiguration.getByRole('button', { name: 'Save relay draft' })
  await expect(saveRelayDraft).toBeEnabled()
  await saveRelayDraft.click()
  await expect(relayConfiguration.getByText('Configuration r3', { exact: true })).toBeVisible()
  expect(relayDraftBody).toMatchObject({
    revision: 2,
    mcpServerIds: [firstServerID, secondServerID],
    mcpRevisionIds: { [firstServerID]: firstMCPRevisionID, [secondServerID]: latestSecondMCPRevisionID },
  })
  await page.getByRole('button', { name: 'Prepare relay update' }).click()
  await expect(relayConfiguration.getByText('claude-coding', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Run relay preflight' }).click()
  await expect(relayConfiguration.getByText('Add · docs', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Apply relay update' }).click()
  await expect(page.getByText(/Relay update queued/)).toBeVisible()
  expect(prepareBody).toEqual({ revisionId: savedDraftConfigurationID })
  expect(preflightBody).toEqual({ revisionId: savedDraftConfigurationID, profileIds: [profileID], mode: 'compatibility' })
  expect(applyBody).toEqual({ revisionId: savedDraftConfigurationID, profileIds: [profileID], mode: 'compatibility', targetRevision, routingHash })

  await expect(page.getByText('New tools', { exact: true })).toBeVisible()
  await expect(page.getByText('Changed tools', { exact: true })).toBeVisible()
  await expect(page.getByText('Removed tools', { exact: true })).toBeVisible()
  await expect(page.getByText('Suspected rename', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Confirm rename · find_code → lookup_code' }).click()
  expect(renameConfirmed).toBeTruthy()
  const mapping = page.getByLabel('Explicit rename mapping')
  const confirmMapping = page.getByRole('button', { name: 'Confirm selected mapping' })
  await expect(confirmMapping).toBeDisabled()
  await mapping.selectOption(ambiguousRenameID)
  await expect(confirmMapping).toBeEnabled()
  await confirmMapping.click()
  expect(ambiguousRenameConfirmed).toBeTruthy()

  const confirmation = page.getByRole('region', { name: `Confirmation · ${profile.name} · search_code` })
  await expect(confirmation).toContainText(argumentHash)
  await expect(confirmation).toContainText('Sensitive string · 17 characters')
  const profileName = confirmation.getByLabel('Exact Profile name')
  const approve = confirmation.getByRole('button', { name: 'Approve once' })
  await expect(approve).toBeDisabled()
  await profileName.fill('Claude-coding')
  await expect(approve).toBeDisabled()
  await profileName.fill(profile.name)
  await expect(approve).toBeEnabled()
  await approve.click()
  expect(confirmationBody).toEqual({ profileName: profile.name, bindingHash })

  await expect(page.getByText('confirmed', { exact: true })).toBeVisible()
  await expect(page.getByText('unknown', { exact: true })).toBeVisible()
  await expect(page.getByText('Check actual state before trying and confirming again')).toBeVisible()
  await expect(page.getByText('12', { exact: true })).toBeVisible()
  await expect(page.locator('input[type="password"]')).toHaveCount(0)
  await expect(page.getByText('raw-secret-marker')).toHaveCount(0)
  await expectNoViewportOverflow(page)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-relay-governance.png`, fullPage: true })
  expect(consoleErrors).toEqual([])
})

test('migrated legacy shared-mcp is hidden from the ordinary Profile list', async ({ page }) => {
  const legacyProfileID = 'b1111111-1111-4111-8111-111111111111'
  const ordinaryProfile = {
    id: 'b2222222-2222-4222-8222-222222222222', currentRevisionId: 'b3333333-3333-4333-8333-333333333333',
    name: 'claude-coding', description: 'Coding profile', clientKind: 'claude', category: 'coding', variant: 'default', migrationState: 'ready',
    revision: 2, canonicalHash: '1'.repeat(64), pendingBindings: false, skillIds: [], mcpServerIds: [], mcpGovernance: [], toolRules: [], effectiveVisibleCount: 0,
    createdAt: now, updatedAt: now,
  }
  const legacyProfile = {
    ...ordinaryProfile, id: legacyProfileID, currentRevisionId: 'b4444444-4444-4444-8444-444444444444', name: 'shared-mcp',
    description: 'Legacy shared MCP profile', clientKind: 'shared', category: 'relay', migrationState: 'compatibility',
  }

  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/v1/auth/session') return route.fulfill({ json: session() })
    if (path === '/api/v1/profiles') return route.fulfill({ json: { items: [legacyProfile, ordinaryProfile] } })
    if (path === '/api/v1/skills') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/mcp/servers') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/targets') return route.fulfill({ json: { items: [] } })
    if (path === '/api/v1/relay/contracts') return route.fulfill({ json: { items: [], renames: [] } })
    if (path === '/api/v1/relay/configuration') return route.fulfill({ json: {
      current: { id: 'b5555555-5555-4555-8555-555555555555', revision: 2, canonicalHash: '2'.repeat(64), mcpServers: [], createdAt: now },
      applied: { id: 'b6666666-6666-4666-8666-666666666666', revision: 1, canonicalHash: '3'.repeat(64), mcpServers: [], createdAt: now },
      mode: 'compatibility', defaultProfileId: null,
      migration: { state: 'compatibility_ready', pendingContractReviews: 0, ambiguousProfiles: 0, legacyProfileId: legacyProfileID, legacyProfileState: 'migrated_relay', restorableSnapshot: true },
      runtimeCapability: { compatible: true, runtimeVersion: '2.15.0-toolhub.1', features: [] },
    } })
    return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: path } } })
  })

  await page.goto('/profiles')
  await expect(page.getByText(ordinaryProfile.name, { exact: true })).toBeVisible()
  await expect(page.getByText(legacyProfile.name, { exact: true })).toHaveCount(0)
})

test('shared relay inventory, controls, and write-only MCP secrets use generation-2 contracts', async ({ page }, testInfo) => {
  const targetID = '11111111-1111-4111-8111-111111111111'
  const nodeID = '22222222-2222-4222-8222-222222222222'
  const serverID = '33333333-3333-4333-8333-333333333333'
  const unavailableMemberID = '99999999-9999-4999-8999-999999999999'
  const relayMemberStatuses = [
    { memberId: '66666666-6666-4666-8666-666666666666', name: 'search', status: 'ready', capabilityKinds: ['tools'], capabilities: { tools: 3, resources: 0, resourceTemplates: 0, prompts: 0 }, checkedAt: now },
    { memberId: unavailableMemberID, name: 'secondary', status: 'unavailable', capabilityKinds: [], capabilities: { tools: 0, resources: 0, resourceTemplates: 0, prompts: 0 }, checkedAt: now, errorCode: 'mcpm_incompatible', errorReason: 'no attributed capability was discovered' },
  ]
  const target = { id: targetID, targetKey: 'local/shared-relay', nodeId: nodeID, nodeName: 'local', nodeKind: 'local', runtime: 'shared-relay', managedUsername: 'runner', writable: true, health: 'blocked', desiredRevision: 3, targetRevision: 'relay-revision', lastScannedAt: now, errorCode: 'mcpm_incompatible', errorReason: 'relay namespace contract is incomplete', relayFailureCount: 3, relaySuspended: true, relayLastMemberCheckAt: now, relayMemberStatuses }
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
      return route.fulfill({ json: { target, targetRevision: 'relay-revision', inventory: { relay: { state: 'active', endpoint: 'http://127.0.0.1:6276/mcp', fixedPort: 6276, systemdEnabled: true, healthy: false, intentionalPaused: false, version: '1.2.3', contract: 'incompatible', memberStatuses: relayMemberStatuses, errorCode: 'mcpm_incompatible', errorReason: 'relay namespace contract is incomplete' }, members: [{ id: 'mcp:search', kind: 'mcp', name: 'search', contentHash: 'c'.repeat(64), protected: false, scope: 'user' }, { id: 'anchor:claude', kind: 'anchor', name: 'toolhub-relay', contentHash: 'd'.repeat(64), protected: false, scope: 'user' }, { id: 'anchor:codex', kind: 'anchor', name: 'toolhub-relay', contentHash: 'e'.repeat(64), protected: false, scope: 'user' }, { id: 'mcp:user-extra', kind: 'mcp', name: 'user-extra', contentHash: 'f'.repeat(64), protected: false, scope: 'user' }] }, desired: { snapshot: { id: '44444444-4444-4444-8444-444444444444', revision: 3, sourceKind: 'profile_apply', profileRevision: 7, manifestHash: 'a'.repeat(64), createdAt: now }, manifest: { schemaVersion: 1, target: { id: targetID, nodeId: nodeID, nodeKind: 'local', runtime: 'shared-relay', managedUsername: 'runner' }, profileId: '55555555-5555-4555-8555-555555555555', profileRevision: 7, skills: [], mcpServers: [{ memberId: '66666666-6666-4666-8666-666666666666', serverId: serverID, revision: 2, name: 'search', transport: 'http', url: server.url, contentHash: server.contentHash }, { memberId: unavailableMemberID, serverId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', revision: 1, name: 'secondary', transport: 'http', url: 'https://secondary.invalid/mcp', contentHash: '9'.repeat(64) }], managedMemberIds: ['66666666-6666-4666-8666-666666666666', unavailableMemberID], relayPort: 6276 } } } })
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
  await expect(page.getByText('suspended', { exact: true })).toBeVisible()
  await expect(page.getByText('incompatible', { exact: true })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'search' }).getByText('ready', { exact: true })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'secondary' }).getByText('unavailable', { exact: true })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'secondary' })).toContainText('mcpm_incompatible')
  for (const label of ['Start', 'Stop', 'Restart', 'Health check']) await expect(page.getByRole('button', { name: label, exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Restart', exact: true }).click()
  await expect(page.getByText(/Relay restart queued/)).toBeVisible()
  expect(relayAction).toBe('restart')
  await expectNoViewportOverflow(page)
  await page.screenshot({ path: `../test-results/${testInfo.project.name}-relay-target.png`, fullPage: true })

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
  await page.getByRole('button', { name: 'Import Skill · formatter' }).click()
  await expect(page.getByText(/Skill import queued/)).toBeVisible()
  expect(skillImportBody).toEqual({ name: 'formatter', expectedRevision: targetRevision, contentHash: skillHash })
  expect(skillIdempotency).toBeTruthy()

  await page.getByRole('button', { name: 'Import MCP from runtime' }).click()
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
  await page.getByRole('button', { name: 'Edit node username' }).click()
  let dialog = page.getByRole('dialog', { name: /Managed username/ })
  await dialog.getByLabel('Node username override').fill('operator')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByText('operator', { exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Edit node username' }).click()
  dialog = page.getByRole('dialog', { name: /Managed username/ })
  await dialog.getByLabel('Node username override').fill('')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByText('runner', { exact: true })).toBeVisible()
  expect(updates).toEqual(['operator', ''])
  await expectNoViewportOverflow(page)
})
