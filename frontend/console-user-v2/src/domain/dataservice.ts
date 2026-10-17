export function dataServiceCommand(
  id: string,
  action: 'start' | 'stop' | 'restart' | 'scale' | 'upgrade' | 'delete',
  body?: unknown,
) {
  return {
    path: `/api/dataservices/${encodeURIComponent(id)}${action === 'delete' ? '' : '/' + action}`,
    options: {
      method:
        action === 'delete'
          ? 'DELETE'
          : action === 'scale' || action === 'upgrade'
            ? 'PUT'
            : 'POST',
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    },
  }
}
