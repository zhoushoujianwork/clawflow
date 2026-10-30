// Only link paths supported by the existing local run route.
export function runDetailUrl(value?: string) {
  const path = value?.match(/^(?:\.\/data\/)?runs\/([^/]+)\/(issue-\d+)\/([^/]+)\/?$/)
  if (path?.slice(1).some(part => part === '.' || part === '..')) return undefined
  return path ? '/runs/' + path.slice(1).map(encodeURIComponent).join('/') : undefined
}
