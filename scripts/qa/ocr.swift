// ocr: print the text macOS Vision recognizes in an image, one line per
// recognized line, top to bottom.
//
//   swift scripts/qa/ocr.swift shot.png
//
// Used by scripts/qa/drive.py for terminals that expose no screen text
// through accessibility (Warp renders with the GPU). OCR output is
// approximate, so drive.py treats EXPECT results from it as advisory and
// the screenshot stays the evidence.
import Foundation
import Vision
import AppKit

guard CommandLine.arguments.count == 2,
      let image = NSImage(contentsOfFile: CommandLine.arguments[1]),
      let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil) else {
    FileHandle.standardError.write("usage: ocr <image>\n".data(using: .utf8)!)
    exit(2)
}

let request = VNRecognizeTextRequest()
request.recognitionLevel = .accurate
request.usesLanguageCorrection = false

do {
    try VNImageRequestHandler(cgImage: cg, options: [:]).perform([request])
} catch {
    FileHandle.standardError.write("ocr failed: \(error)\n".data(using: .utf8)!)
    exit(1)
}

// Vision's origin is bottom-left; sort top to bottom, then left to right.
let lines = (request.results ?? [])
    .compactMap { obs -> (CGRect, String)? in
        guard let top = obs.topCandidates(1).first else { return nil }
        return (obs.boundingBox, top.string)
    }
    .sorted { a, b in
        if abs(a.0.midY - b.0.midY) > 0.01 { return a.0.midY > b.0.midY }
        return a.0.minX < b.0.minX
    }
for (_, s) in lines { print(s) }
