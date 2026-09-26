export function requireAuth(req: any, res: any, next: any) {
  if (!req.headers.authorization) {
    return res.status(401).json({ error: 'unauthorized' });
  }
  next();
}
