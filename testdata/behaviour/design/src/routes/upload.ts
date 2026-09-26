import express from 'express';
import multer from 'multer';
import { requireAuth } from '../middleware/auth';
import { handleUpload } from '../handlers/upload';

const router = express.Router();
const upload = multer({ dest: 'uploads/' });

// Uploads go through auth, then multer's single-file parsing, then the
// handler. Rate limiting is added here by the design session below.
//
//
//
//
//
//
//
//
//
//
//
router.post('/upload', requireAuth, upload.single('file'), handleUpload);

export default router;
