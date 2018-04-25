from datetime import datetime
import hashlib
import json
import os
import shutil
import subprocess

import dateutil.parser as date_parser
import exifread
import rawkit

import photodb.util as util


class EXIFMetadata(object):

    def __init__(self, tags):
        self.tags = tags

        width, height = get_exif_dimensions(tags)

        self.summary = {
            'make': get_exif_make(tags),
            'model': get_exif_model(tags),
            'focal_length': get_exif_focal_length(tags),
            'width': width,
            'height': height,
            'timestamp': get_exif_timestamp(tags),
            'iso': get_exif_iso(tags),
            'shutter_speed': get_exif_shutter(tags),
            'aperture': get_exif_aperture(tags)
        }

    def __repr__(self):
        return repr(self.summary)


def _get_summary_meta(tag):
    @property
    def getter(self):
        return self.metadata.summary[tag]
    return getter


class Photo(object):

    def __init__(self, path):
        self.path = path
        self.file_ext = util.get_file_extension(self.path)

        self.metadata_type = util.get_type_from_ext(self.file_ext)
        try:
            self.metadata = read_exif(self.path)
            self.is_valid = True
            self.metadata_exc = None
        except subprocess.CalledProcessError as e:
            self.metadata = None
            self.is_valid = False
            self.metadata_exc = e

    @property
    def checksum(self):
        return util.get_checksum(self.path)

    @property
    def size(self):
        return os.stat(self.path).st_size

    width = _get_summary_meta('width')
    height = _get_summary_meta('height')
    make = _get_summary_meta('make')
    model = _get_summary_meta('model')
    focal_length = _get_summary_meta('focal_length')
    iso = _get_summary_meta('iso')
    shutter_speed = _get_summary_meta('shutter_speed')
    aperture = _get_summary_meta('aperture')
    timestamp = _get_summary_meta('timestamp')


# ----------------------------------------------------------------------
# EXIF metadata processing


def read_exif(path):
    exiftool_output = subprocess.check_output(['exiftool', '-j', path])
    tags = json.loads(exiftool_output)[0]

    return EXIFMetadata(tags)


def get_exif_iso(tags):
    try:
        return tags['ISO']
    except KeyError:
        return -1


def get_exif_shutter(tags):
    if 'ShutterSpeedValue' in tags:
        return tags['ShutterSpeedValue']
    else:
        try:
            return tags['ShutterSpeed']
        except KeyError:
            return 'unknown'


def get_exif_aperture(tags):
    try:
        return tags['FNumber']
    except KeyError:
        return -1


def get_exif_dimensions(tags):
    if 'RawImageFullWidth' in tags:
        width = tags['RawImageFullWidth']
        height = tags['RawImageFullHeight']
    else:
        width = tags['ImageWidth']
        height = tags['ImageHeight']

    return width, height


def get_exif_make(tags):
    try:
        return tags['Make']
    except KeyError:
        return 'unknown'


def get_exif_model(tags):
    try:
        return tags['Model']
    except KeyError:
        return 'unknown'


def get_exif_focal_length(tags):
    try:
        return tags['FocalLength']
    except KeyError:
        return r'unknown'


EXIF_TIMESTAMP_FORMAT = '%Y:%m:%d %H:%M:%S'


def get_exif_timestamp(tags):
    try:
        timestamp = tags['CreateDate']
    except KeyError:
        try:
            timestamp = tags['FileModifyDate']
        except KeyError:
            return datetime(1980, 1, 1)

    try:
        return datetime.strptime(timestamp, EXIF_TIMESTAMP_FORMAT)
    except ValueError:
        return date_parser.parse(timestamp)
